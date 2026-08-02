require "json"
require "open3"

require_relative "remote"

module Twin
  # One YAML block from a sync-file, enriched with live filesystem state.
  Job = Struct.new(
    :program, :path, :description, :active, :excludes, :owned, :label,
    :source, :target, :cmd, :delete, :render, :render_outdated, :target_path_field, :sync_file,
    :source_exists, :target_exists, :source_mtime, :target_mtime, :conflict,
    :target_unreachable,
    keyword_init: true,
  ) do
    def source_path = File.join(source, path)
    def target_path = File.join(target, target_path_field || path)
    def remote?     = Twin::Remote.remote?(target)

    # Everything rsync must not touch: Exclude (not part of the sync at all)
    # plus Own (part of the scope, but the target owns it).
    def all_excludes = excludes + (owned || [])

    def status
      return :disabled if active != 1
      return :unreachable if target_unreachable
      return :both_missing if !source_exists && !target_exists
      return :missing_source unless source_exists
      return :missing_target unless target_exists
      # Render jobs compare by content, not mtime — a rendered target's mtime
      # bears no relation to the template's.
      return render_outdated ? :source_newer : :in_sync if render
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
      %i[unreachable both_missing missing_source missing_target target_newer source_newer disabled in_sync]
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
      vars = cfg.var_map
      jobs = records.filter_map do |r|
        context = "#{r["Program"]} in #{File.basename(r["_note_file"].to_s)}"
        build_job(Twin::Template.substitute_record(r, vars, context: context), vars)
      end
      fill_remote_stats(jobs)
      jobs
    end

    # Remote targets can't be stat'ed locally — batch them into one ssh
    # round-trip per host. A failed ssh marks the jobs unreachable instead
    # of aborting the scan (local jobs stay usable).
    def fill_remote_stats(jobs)
      jobs.select { |j| j.remote? && j.active == 1 }
          .group_by { |j| Twin::Remote.split(j.target).first }
          .each do |host, host_jobs|
        stats = Twin::Remote.stat_paths(host, host_jobs.map { |j| Twin::Remote.split(j.target_path).last })
        host_jobs.each do |j|
          rpath = Twin::Remote.split(j.target_path).last
          if stats.nil?
            j.target_unreachable = true
            next
          end
          mtime = stats[rpath]
          j.target_exists = !mtime.nil?
          j.target_mtime  = mtime
          j.conflict      = j.source_exists && mtime && j.source_mtime &&
                            mtime - j.source_mtime >= 60
        end
      end
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

    def build_job(r, vars = {})
      path              = r["Path"].to_s
      source            = r["Source"].to_s
      target            = r["Target"].to_s
      target_path_field = r["Target-Path"].then { |v| v.to_s.empty? ? nil : v.to_s }
      return nil if path.empty? || source.empty? || target.empty?

      render   = r["Render"] == true
      excludes = split_list(r["Exclude"])
      # Own: paths inside the sync scope that the TARGET owns — machine-specific
      # config the source must never clobber. Same rsync effect as Exclude, but
      # kept apart so `status` can name the intent instead of hiding it among
      # build artefacts and .DS_Store.
      owned    = split_list(r["Own"])
      remote   = Twin::Remote.remote?(target)

      if render && remote
        raise "#{r["Program"]}: Render is not supported for remote targets (#{target})"
      end

      src_full = File.join(source, path)
      tgt_full = File.join(target, target_path_field || path)
      src_exists, src_mtime = stat(src_full)
      # Remote targets are stat'ed in one batched ssh call after all jobs are
      # built (fill_remote_stats) — until then they read as missing.
      tgt_exists, tgt_mtime = remote ? [false, nil] : stat(tgt_full)

      # Render jobs: status is content-based (mtime is meaningless for a rendered
      # target). conflict stays false so the mtime conflict-warning skips them.
      render_outdated = render ? render_outdated?(src_full, tgt_full, vars, path) : nil

      # Same 60s tolerance as Job#status, so mtime jitter never flags a conflict.
      conflict = !render && src_exists && tgt_exists && tgt_mtime && src_mtime &&
                 tgt_mtime - src_mtime >= 60

      Job.new(
        program:          r["Program"].to_s,
        path:             path,
        description:      r["Description"].to_s,
        active:           (r["Active"] || 0).to_i,
        excludes:         excludes,
        owned:            owned,
        label:            r["Label"].to_s,
        source:           source,
        target:           target,
        cmd:              r["Cmd"].to_s,
        delete:           r["Delete"] == true,
        render:           render,
        render_outdated:  render_outdated,
        target_path_field: target_path_field,
        sync_file:        r["_note_file"].to_s,
        source_exists:    src_exists,
        target_exists:    tgt_exists,
        source_mtime:     src_mtime,
        target_mtime:     tgt_mtime,
        conflict:         !!conflict,
        target_unreachable: false,
      )
    end

    def stat(path)
      st = File.stat(path)
      [true, st.mtime]
    rescue Errno::ENOENT, Errno::EACCES
      [false, nil]
    end

    # Comma-separated block field → array of trimmed, non-empty entries.
    def split_list(value)
      (value || "").split(",").map(&:strip).reject(&:empty?)
    end

    # For a render job: is the target out of date with the rendered template?
    # nil when source is missing/a directory (status falls through to those).
    # True when target is absent or content differs, or the template can't be
    # rendered (unresolved token) — i.e. needs attention.
    def render_outdated?(src_full, tgt_full, vars, context)
      return nil unless File.file?(src_full)
      rendered = Twin::Template.render_file(src_full, vars, context: context)
      !File.exist?(tgt_full) || File.binread(tgt_full) != rendered
    rescue
      true
    end
  end
end
