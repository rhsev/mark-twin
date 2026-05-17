require "open3"
require "tempfile"

require_relative "preview"

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
      missing_target: "!",
      missing_source: "!",
      both_missing:   "✗",
      disabled:       "·",
    }.freeze

    STATUS_COLORS = {
      source_newer:   "\e[33m",   # yellow
      target_newer:   "\e[36m",   # cyan
      in_sync:        "\e[32m",   # green
      missing_target: "\e[31m",   # red
      missing_source: "\e[31m",   # red
      both_missing:   "\e[31m",   # red
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

    # Multi-line entries (NUL-separated). Header line per program, indented
    # body lines per job. Returns the selected Program or nil.
    def pick_program(programs)
      raise "fzf not found in PATH" unless which("fzf")
      return nil if programs.empty?

      name_width = programs.map { |p| p.name.length }.max
      path_width = programs.flat_map { |p| p.jobs.map { |j| j.path.length } }.max

      entries = programs.map { |p| render_program_entry(p, name_width, path_width) }
      input   = entries.join("\0")

      fzf = [
        "fzf",
        "--read0", "--ansi", "--no-multi",
        "--prompt=program> ",
        "--height=100%", "--reverse",
        "--no-sort",
        "--color=bg+:-1,hl+:reverse",
      ]

      output, status = Open3.capture2(SH_ENV, *fzf, stdin_data: input)
      return nil unless status.success?
      return nil if output.strip.empty?

      first_line = output.lines.first.to_s
      programs.find { |p| first_line.include?(" #{p.name} ") || first_line.rstrip.end_with?(p.name) || first_line.include?(p.name) }
    end

    # ── Stufe 2: path multi-picker ────────────────────────────────────────────

    # Multi-select over the jobs of one program. Right pane shows the
    # apex-rendered compact view (frontmatter + intro + selected block).
    # Returns array of selected Jobs, :back on ESC, [] on empty confirm.
    def pick_paths(program, cfg)
      jobs = program.jobs
      return [] if jobs.empty?

      path_width = jobs.map { |j| j.path.length }.max
      tempfiles  = write_compact_previews(program, jobs)

      rows = jobs.each_with_index.map do |j, i|
        icon  = STATUS_ICONS[j.status] || "?"
        delta = format_delta(j.source_mtime, j.target_mtime)
        line  = "#{icon}  #{j.path.ljust(path_width)}  #{delta}"
        "#{i}\t#{colorize(j.status, line)}"
      end

      preview_cmd = build_apex_preview_cmd(tempfiles, cfg)

      fzf = [
        "fzf",
        "--multi", "--ansi",
        "--delimiter=\t", "--with-nth=2",
        "--prompt=#{program.name} > ",
        "--header=#{program.name} — Tab toggles, Enter confirms",
        "--preview=#{preview_cmd}",
        "--preview-window=right:60%:wrap",
        "--height=100%", "--reverse",
        "--bind=ctrl-a:select-all",
        "--color=bg+:-1,hl+:reverse",
      ]

      output, status = Open3.capture2(SH_ENV, *fzf, stdin_data: rows.join("\n"))
      return :back if status.exitstatus == 130   # ESC / Ctrl-C
      return [] unless status.success?
      return [] if output.strip.empty?

      output.lines.filter_map do |line|
        idx = line.split("\t", 2).first&.to_i
        jobs[idx] if idx
      end
    ensure
      tempfiles&.each_value { |path| File.unlink(path) rescue nil }
    end

    # Write per-job compact-preview markdown to tempfiles. Returns {idx => path}.
    def write_compact_previews(program, jobs)
      result = {}
      jobs.each_with_index do |job, i|
        compact = Preview.extract_compact(program.sync_file, job.path)
        f = Tempfile.new(["twin-#{i}-", ".md"])
        f.write(compact)
        f.close
        result[i] = f.path
      end
      result
    end

    def build_apex_preview_cmd(tempfiles, cfg)
      # Map idx → file via a small TSV, awk picks the right one for {1}.
      mapfile = Tempfile.new(["twin-map-", ".tsv"])
      tempfiles.each { |i, path| mapfile.puts("#{i}\t#{path}") }
      mapfile.close
      ObjectSpace.define_finalizer(mapfile, ->(_) { File.unlink(mapfile.path) rescue nil })

      render_cmd = pick_renderer(cfg)

      %(F=$(awk -v id={1} -F'\\t' '$1==id {print $2}' #{mapfile.path}); ) +
        %([ -n "$F" ] && #{render_cmd})
    end

    # Pick the first available markdown renderer.
    def pick_renderer(cfg)
      if which("apex")
        args = ["--plugins", "-t", "terminal256"]
        args += ["--theme", cfg.apex_theme]                         if cfg.apex_theme
        args += ["--width", cfg.apex_width.to_s]                    if cfg.apex_width
        args += ["--code-highlight", cfg.apex_code_highlight]       if cfg.apex_code_highlight
        args += ["--code-highlight-theme", cfg.apex_code_highlight_theme] if cfg.apex_code_highlight_theme
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
      sync_file = File.basename(program.sync_file.to_s)
      count     = program.active_jobs.size
      total     = program.jobs.size
      header    = "#{icon}  #{bold(program.name.ljust(name_width))}    " \
                  "#{dim("(#{count}/#{total})")}    #{dim("[#{sync_file}]")}"

      body = program.jobs.map do |j|
        icon  = STATUS_ICONS[j.status] || "?"
        delta = format_delta(j.source_mtime, j.target_mtime)
        line  = "    #{icon}  #{j.path.ljust(path_width)}  #{delta}"
        colorize(j.status, line)
      end

      ([header] + body).join("\n")
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
