require "json"
require "open3"

module Twin
  # One YAML block from a sync-file, enriched with live filesystem state.
  Job = Struct.new(
    :program, :path, :description, :active, :excludes, :label,
    :source, :target, :cmd, :sync_file,
    :source_exists, :target_exists, :source_mtime, :target_mtime, :conflict,
    keyword_init: true,
  ) do
    def source_path = File.join(source, path)
    def target_path = File.join(target, path)

    def status
      return :disabled if active != 1
      return :both_missing if !source_exists && !target_exists
      return :missing_source unless source_exists
      return :missing_target unless target_exists
      return :target_newer if conflict
      return :in_sync if source_mtime.nil? || target_mtime.nil?
      delta = source_mtime - target_mtime
      return :in_sync if delta.abs < 60
      delta > 0 ? :source_newer : :target_newer
    end
  end

  # A logical group of Jobs sharing a Program name (e.g. "Matterbase" can have
  # multiple paths). Selection unit in the picker.
  Program = Struct.new(:name, :jobs, keyword_init: true) do
    def sync_file = jobs.first&.sync_file
    def label     = jobs.first&.label

    def description
      jobs.map(&:description).reject(&:empty?).uniq.join(" / ")
    end

    # Aggregate status across jobs — worst first.
    def status
      states = jobs.map(&:status)
      %i[both_missing missing_source missing_target target_newer source_newer disabled in_sync]
        .find { |s| states.include?(s) } || :in_sync
    end

    def active_jobs = jobs.select { |j| j.active == 1 }

    def newest_source_mtime = jobs.map(&:source_mtime).compact.max
    def newest_target_mtime = jobs.map(&:target_mtime).compact.max
  end

  module Scanner
    module_function

    def load_jobs(cfg, scan_path: nil)
      raise "grubber not found in PATH" unless system("command -v grubber > /dev/null 2>&1")

      dir = scan_path || cfg.sync_dir
      stdout, stderr, status = Open3.capture3(
        "grubber", "extract", dir, "-b", "--format", "json"
      )
      raise "grubber: #{stderr.force_encoding('UTF-8').strip}" unless status.success?

      begin
        records = JSON.parse(stdout.force_encoding("UTF-8"))
      rescue JSON::ParserError => e
        raise "grubber returned invalid JSON: #{e.message}"
      end
      records.filter_map { |r| build_job(r) }
    end

    def load_programs(cfg, file: nil, label: nil, show_all: false)
      scan_path, name_filter = resolve_file_arg(cfg, file)
      jobs = load_jobs(cfg, scan_path: scan_path)
      jobs = jobs.select { |j| j.sync_file.include?(name_filter) } if name_filter
      jobs = jobs.select { |j| j.label == label }                   if label && !label.empty?
      jobs = jobs.select { |j| j.active == 1 }                     unless show_all
      group(jobs)
    end

    # Returns [scan_path, name_filter] for a given file argument.
    # - nil / empty        → [nil, nil]           scan sync_dir, no filter
    # - path to a dir      → [dir, nil]            scan that dir, no filter
    # - path to a file     → [dirname, basename]   scan parent dir, filter by filename
    # - bare name (no /)   → [nil, name]           scan sync_dir, filter by name
    def resolve_file_arg(cfg, file)
      return [nil, nil] if file.nil? || file.empty?
      if file.include?("/") || file == "." || file == ".."
        expanded = File.expand_path(file)
        return [expanded, nil]                             if File.directory?(expanded)
        return [File.dirname(expanded), File.basename(expanded)] if File.file?(expanded)
        raise "not found: #{file}"
      end
      [nil, file]
    end

    def group(jobs)
      jobs.group_by { |j| [j.program, j.sync_file] }
          .map { |(name, _file), js| Program.new(name: name, jobs: js) }
    end

    def build_job(r)
      path   = r["Path"].to_s
      source = r["Source"].to_s
      target = r["Target"].to_s
      return nil if path.empty? || source.empty? || target.empty?

      excludes = (r["Exclude"] || "").split(",").map(&:strip).reject(&:empty?)

      src_full = File.join(source, path)
      tgt_full = File.join(target, path)
      src_exists, src_mtime = stat(src_full)
      tgt_exists, tgt_mtime = stat(tgt_full)
      # Same 60s tolerance as Job#status, so mtime jitter never flags a conflict.
      conflict = src_exists && tgt_exists && tgt_mtime && src_mtime &&
                 tgt_mtime - src_mtime >= 60

      Job.new(
        program:       r["Program"].to_s,
        path:          path,
        description:   r["Description"].to_s,
        active:        (r["Active"] || 0).to_i,
        excludes:      excludes,
        label:         r["Label"].to_s,
        source:        source,
        target:        target,
        cmd:           r["Cmd"].to_s,
        sync_file:     r["_note_file"].to_s,
        source_exists: src_exists,
        target_exists: tgt_exists,
        source_mtime:  src_mtime,
        target_mtime:  tgt_mtime,
        conflict:      !!conflict,
      )
    end

    def stat(path)
      st = File.stat(path)
      [true, st.mtime]
    rescue Errno::ENOENT, Errno::EACCES
      [false, nil]
    end
  end
end
