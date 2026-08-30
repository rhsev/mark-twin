require "open3"
require "tempfile"

require_relative "preview"
require_relative "conflict"

module Twin
  # Two-step fzf picker:
  #   pick_program → tabular multi-line list, no preview
  #   pick_paths   → multi-select within one program, glow renders the sync-file
  module Picker
    module_function

    STATUS_ICONS = {
      source_newer:   "→",
      target_newer:   "←",
      in_sync:        "✓",
      # Directory jobs in stage 1: their mtimes prove nothing, and the
      # dry-runs that would prove something are too slow for the full picker.
      # No claim instead of a wrong one — stage 2 verifies its one program
      # on open, and `twin status` has the full verdict.
      unverified:     "∘",
      missing_target: "!",
      missing_source: "!",
      both_missing:   "✗",
      unreachable:    "?",
      disabled:       "·",
    }.freeze

    STATUS_COLORS = {
      source_newer:   "\e[33m",   # yellow
      target_newer:   "\e[36m",   # cyan
      in_sync:        "\e[32m",   # green
      unverified:     "\e[2m",    # dim
      missing_target: "\e[31m",   # red
      missing_source: "\e[31m",   # red
      both_missing:   "\e[31m",   # red
      unreachable:    "\e[31m",   # red
      disabled:       "\e[2m",    # dim
    }.freeze

    BOLD  = "\e[1m"
    DIM   = "\e[2m"
    RESET = "\e[0m"

    SH_ENV = { "SHELL" => "/bin/sh" }.freeze

    def colorize(status, text) = "#{STATUS_COLORS[status] || ''}#{text}#{RESET}"
    def dim(text)              = "#{DIM}#{text}#{RESET}"
    def bold(text)             = "#{BOLD}#{text}#{RESET}"

    # ── Stufe 1: program picker ───────────────────────────────────────────────

    # A trailing bracket group ties a program to its base name by convention:
    # "livesync [agent]" lists under "livesync". Only the suffix form counts —
    # brackets elsewhere are just a name.
    BRACKET_SUFFIX = /\s*\[[^\]]*\]\z/

    def base_name(name) = name.sub(BRACKET_SUFFIX, "").strip

    # One stage-1 entry per base name (case-insensitive), even when it appears
    # in several sync-files or as bracketed variants — "everything of
    # fileview" is one decision. The list is sorted by name; document order
    # only orders the *jobs*, program order carries no meaning. Purely a
    # picker view: the data model, `status`, `sync -p` and JSON keep the
    # per-file programs. Jobs stay grouped per file in document order, so a
    # file's closing Cmd block still fires after its own paths.
    def merge_programs(programs)
      programs.group_by { |p| base_name(p.name).downcase }.values.map do |group|
        next group.first if group.size == 1
        name = group.find { |p| !p.name.match?(BRACKET_SUFFIX) }&.name ||
               base_name(group.first.name)
        Program.new(name: name, jobs: group.flat_map(&:jobs))
      end.sort_by { |p| p.name.downcase }
    end

    # Multi-line entries (NUL-separated). Header line per program, indented
    # body lines per job. Returns the selected Program or nil.
    def pick_program(programs)
      raise "fzf not found in PATH" unless which("fzf")
      return nil if programs.empty?

      name_width = programs.map { |p| p.name.length }.max
      path_width = programs.flat_map { |p| p.jobs.map { |j| j.path.length } }.max

      entries = programs.each_with_index.map do |p, i|
        "#{i}\t#{render_program_entry(p, name_width, path_width)}"
      end
      input = entries.join("\0")

      fzf = [
        "fzf",
        "--read0", "--ansi", "--no-multi",
        "--delimiter=\t", "--with-nth=2..",
        "--prompt=program> ",
        "--height=100%", "--reverse",
        "--no-sort",
        "--color=bg+:-1,hl+:reverse",
      ]

      output, status = Open3.capture2(SH_ENV, *fzf, stdin_data: input)
      return nil unless status.success?
      return nil if output.strip.empty?

      idx = output.split("\t", 2).first
      programs[idx.to_i] if idx&.match?(/\A\d+\z/)
    end

    # ── Stufe 2: path multi-picker ────────────────────────────────────────────

    # Multi-select over the jobs of one program. Right pane shows the
    # apex-rendered compact view (frontmatter + intro + selected block).
    # Returns array of selected Jobs, :back on ESC, [] on empty confirm.
    def pick_paths(program, cfg)
      jobs = program.jobs
      return [] if jobs.empty?

      # Stage 2 covers one program, so the dry-runs that are too slow for the
      # whole picker are affordable here — directory rows get a real verdict
      # instead of `∘`. Already-verified jobs are skipped on re-entry.
      if Conflict.drift_candidates(jobs).any?
        $stderr.print dim("verifying directories …")
        Conflict.fill_drift(cfg, jobs)
        $stderr.print "\r\e[K"
      end

      path_width = jobs.map { |j| j.path.length }.max
      tempfiles  = write_compact_previews(program, jobs)
      preview_cmd, mapfile = build_apex_preview_cmd(tempfiles, cfg)

      # A merged program spans sync-files (and bracketed variants); a dim
      # section row marks each block, named by its file — plus the original
      # program name when it differs from the merged one. Section rows carry
      # "-" instead of an index — fzf can't make them unselectable, but they
      # map to no job, so toggling one is inert.
      section = ->(j) { [j.program, j.sync_file] }
      multi = jobs.map(&section).uniq.size > 1
      rows = []
      prev = nil
      jobs.each_with_index do |j, i|
        if multi && section.(j) != prev
          file  = File.basename(j.sync_file.to_s)
          label = j.program == program.name ? file : "#{j.program} · #{file}"
          rows << "-\t#{dim("[#{label}]")}"
          prev = section.(j)
        end
        icon = STATUS_ICONS[j.status] || "?"
        line = "#{icon}  #{j.path.ljust(path_width)}  #{job_delta(j)}"
        rows << "#{i}\t#{colorize(j.status, line)}"
      end

      header = "#{program.name} — Tab toggles, Enter confirms"
      unverified = jobs.select { |j| j.verify == false }
      unless unverified.empty?
        header += "\n\e[33m⚠ content checks off (Verify: false): " \
                  "#{unverified.map(&:path).join(", ")}\e[0m"
      end

      fzf = [
        "fzf",
        "--multi", "--ansi",
        "--delimiter=\t", "--with-nth=2",
        "--prompt=#{program.name} > ",
        "--header=#{header}",
        "--preview=#{preview_cmd}",
        "--preview-window=right:60%:wrap",
        "--height=100%", "--reverse",
        "--bind=ctrl-a:select-all",
        "--color=bg+:-1,hl+:reverse",
      ]

      loop do
        output, status = Open3.capture2(SH_ENV, *fzf, stdin_data: rows.join("\n"))
        return :back if status.exitstatus == 130   # ESC / Ctrl-C
        return [] unless status.success?
        return [] if output.strip.empty?

        selected = output.lines.filter_map do |line|
          idx = line.split("\t", 2).first
          jobs[idx.to_i] if idx&.match?(/\A\d+\z/)
        end
        return selected unless selected.empty?
        # Enter landed on a section header (or only headers were toggled):
        # nothing real was accepted — reopen rather than read it as "exit".
      end
    ensure
      tempfiles&.each_value { |f| f.close! rescue nil }
      mapfile&.close! rescue nil
    end

    # Write per-job compact-preview markdown to tempfiles. Returns
    # {idx => Tempfile}. The Tempfile objects (not just their paths) must stay
    # referenced while fzf runs: a GC'd Tempfile unlinks its file, and the
    # preview command would find nothing to render. Each job's excerpt comes
    # from its own sync-file — a merged program spans several.
    def write_compact_previews(program, jobs)
      result = {}
      jobs.each_with_index do |job, i|
        compact = Preview.extract_compact(job.sync_file, job.path)
        f = Tempfile.new(["twin-#{i}-", ".md"])
        f.write(compact)
        f.close
        result[i] = f
      end
      result
    end

    # Returns [preview_cmd, mapfile]; the caller holds the Tempfile until fzf
    # is done (GC would unlink it) and closes it afterwards.
    def build_apex_preview_cmd(tempfiles, cfg)
      # Map idx → file via a small TSV, awk picks the right one for {1}.
      mapfile = Tempfile.new(["twin-map-", ".tsv"])
      tempfiles.each { |i, f| mapfile.puts("#{i}\t#{f.path}") }
      mapfile.close

      render_cmd = pick_renderer(cfg)

      cmd = %(F=$(awk -v id={1} -F'\\t' '$1==id {print $2}' #{mapfile.path}); ) +
            %([ -n "$F" ] && #{render_cmd})
      [cmd, mapfile]
    end

    # Pick the first available markdown renderer.
    def pick_renderer(cfg)
      if which("apex")
        args = ["--plugins", "-t", "terminal256"]
        args += ["--theme", shellesc(cfg.apex_theme)]                         if cfg.apex_theme
        args += ["--width", shellesc(cfg.apex_width)]                         if cfg.apex_width
        args += ["--code-highlight", shellesc(cfg.apex_code_highlight)]       if cfg.apex_code_highlight
        args += ["--code-highlight-theme", shellesc(cfg.apex_code_highlight_theme)] if cfg.apex_code_highlight_theme
        (["apex", '"$F"'] + args).join(" ") + " 2>/dev/null"
      elsif which("glow")
        %(glow -s dark "$F" 2>/dev/null)
      elsif which("bat")
        %(bat --color=always --language=markdown --style=plain "$F" 2>/dev/null)
      else
        %(cat "$F")
      end
    end

    def which(cmd)
      ENV.fetch("PATH", "").split(File::PATH_SEPARATOR).any? do |dir|
        File.executable?(File.join(dir, cmd))
      end
    end

    # ── Helpers ───────────────────────────────────────────────────────────────

    def render_program_entry(program, name_width, path_width)
      icon      = colorize(program.status, STATUS_ICONS[program.status] || "?")
      files     = program.jobs.map { |j| File.basename(j.sync_file.to_s) }.uniq.join(" · ")
      count     = program.active_jobs.size
      total     = program.jobs.size
      header    = "#{icon}  #{bold(program.name.ljust(name_width))}    " \
                  "#{dim("(#{count}/#{total})")}    #{dim("[#{files}]")}"

      body = program.jobs.map do |j|
        icon  = STATUS_ICONS[j.status] || "?"
        line  = "    #{icon}  #{j.path.ljust(path_width)}  #{job_delta(j)}"
        colorize(j.status, line)
      end

      ([header] + body).join("\n")
    end

    # A directory's mtime delta would mislead (it moves on every sync), so
    # directory jobs show none.
    def job_delta(job)
      job.directory ? "" : format_delta(job.source_mtime, job.target_mtime)
    end

    def format_delta(sm, tm)
      return "" if sm.nil? || tm.nil?
      seconds = (sm - tm).to_i
      return "in sync" if seconds.abs < 60
      label = seconds > 0 ? "src" : "tgt"
      abs = seconds.abs
      unit =
        if abs >= 86400 then "#{abs / 86400}d"
        elsif abs >= 3600 then "#{abs / 3600}h"
        else "#{abs / 60}m"
        end
      "#{label} +#{unit}"
    end

    def shellesc(s)
      "'" + s.to_s.gsub("'", %q['\\''])  + "'"
    end
  end
end
