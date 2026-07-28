require "optparse"
require "json"
require "set"
require "time"  # Time#iso8601 for --json output

require_relative "config"
require_relative "scanner"
require_relative "sync"
require_relative "journal"
require_relative "add"
require_relative "picker"

module Twin
  module CLI
    module_function

    USAGE = <<~TXT
      twin — sync configuration files between machines

      USAGE:
        twin                       interactive picker (all sync-files)
        twin <name>                picker — sync-file by name in sync_dir
        twin <path>                picker — file or directory (absolute or relative)
        twin list   [--all] [--label X] [--file X] [--json]
        twin status [--all] [--label X] [--file X] [--json]
        twin sync   [-p PATTERN] [--label X] [--file X] [--all] [--dry-run]
                    [--quiet] [--skip-unavailable]
        twin add <path>            scaffold a new sync entry for a local path
        twin log    [-n N] [--json]  recent journal entries (default 20)
        twin doctor                check tools, renderers, and sync targets
        twin --help                show this message

      FILE ARGUMENT:
        bare name (no /)  → matched by substring against sync-file names
        contains /        → resolved as path; file or directory both work

      CONFIG:
        ~/.config/twin/config.yaml
        TWIN_SYNC_DIR  overrides sync_dir
        TWIN_CONFIG    overrides config path
    TXT

    def run(argv)
      cfg = Twin::Config.load
      cfg.validate!

      first = argv.first
      case first
      when nil
        pick_and_sync(cfg, file: nil)
      when "list"     then cmd_list(cfg, argv.drop(1))
      when "status"   then cmd_status(cfg, argv.drop(1))
      when "sync"     then cmd_sync(cfg, argv.drop(1))
      when "add"      then cmd_add(cfg, argv.drop(1))
      when "log"      then cmd_log(argv.drop(1))
      when "doctor"   then cmd_doctor(cfg)
      when "-h", "--help", "help"
        puts USAGE
      when /\A-/
        warn "unknown option: #{first}"
        warn "Run 'twin --help' for usage."
        exit 1
      else
        # Treat as file.md or directory path
        pick_and_sync(cfg, file: first)
      end
    rescue => e
      warn "error: #{e.message}"
      exit 1
    end

    # ── picker → sync ──────────────────────────────────────────────────────────

    def pick_and_sync(cfg, file:)
      programs = Scanner.load_programs(cfg, file: file, show_all: false)
      if programs.empty?
        warn "no active programs found#{" in #{file}" if file}"
        return
      end

      selected_key = nil  # [name, sync_file] of last program — used to re-enter
      loop do
        program =
          if selected_key
            programs.find { |p| [p.name, p.sync_file] == selected_key }
          else
            Picker.pick_program(programs)
          end
        return unless program

        jobs = Picker.pick_paths(program, cfg)
        if jobs == :back               # ESC in stage 2 → back to stage 1
          selected_key = nil
          next
        end
        if jobs.empty?                 # Enter without selection → exit
          return
        end

        sync_jobs(cfg, program, jobs)

        print "\npress Enter to continue, q to quit "
        $stdout.flush
        break if $stdin.gets&.strip == "q"

        # reload so status reflects what was just synced, stay on this program
        programs     = Scanner.load_programs(cfg, file: file, show_all: false)
        selected_key = [program.name, program.sync_file]
      end
    end

    # ── list / status ──────────────────────────────────────────────────────────

    def cmd_list(cfg, args)
      opts = parse_filter_opts(args)
      programs = Scanner.load_programs(cfg, **opts.slice(:file, :label, :show_all))

      if opts[:json]
        puts JSON.pretty_generate(programs.map { |p| program_to_hash(p) })
        return
      end

      programs.each do |p|
        mark = p.status == :disabled ? "–" : "✓"
        puts "#{mark}  #{p.name}  — #{p.description}"
      end
    end

    def cmd_status(cfg, args)
      opts = parse_filter_opts(args)
      programs = Scanner.load_programs(cfg, **opts.slice(:file, :label, :show_all))

      if opts[:json]
        puts JSON.pretty_generate(programs.map { |p| program_to_hash(p) })
        return
      end

      tty = $stdout.tty?
      programs.each do |p|
        icon = Picker::STATUS_ICONS[p.status] || "?"
        icon = Picker.colorize(p.status, icon) if tty
        name = tty ? Picker.bold(p.name) : p.name
        puts "#{icon}  #{name}"
        p.jobs.each do |j|
          src = j.source_exists ? j.source_mtime.strftime("%Y-%m-%d %H:%M:%S") : "(not found)"
          tgt = j.target_exists ? j.target_mtime.strftime("%Y-%m-%d %H:%M:%S") : "(not found)"
          tgt = "(unreachable)" if j.target_unreachable
          conflict = j.conflict ? (tty ? "  #{Picker.colorize(:target_newer, "!")}" : "  !") : ""
          puts "    #{j.path}#{conflict}"
          puts "      src #{src}"
          puts "      dst #{tgt}"
        end
      end
    end

    # ── sync ───────────────────────────────────────────────────────────────────

    def cmd_sync(cfg, args)
      opts = parse_sync_opts(args)
      programs = Scanner.load_programs(cfg, **opts.slice(:file, :label, :show_all))

      if opts[:pattern]
        programs = programs.select { |p| p.name.downcase.include?(opts[:pattern].downcase) }
      end

      if programs.empty?
        puts "no matching programs" unless opts[:quiet]
        return
      end

      results = programs.map do |p|
        sync_jobs(cfg, p, p.active_jobs,
                  dry_run: opts[:dry_run], quiet: opts[:quiet],
                  skip_unavailable: opts[:skip_unavailable])
      end
      exit 1 unless results.all?
    end

    # Sync the given jobs. Returns true when every attempted job succeeded.
    # quiet:            print only conflicts, errors, and jobs that changed something
    # skip_unavailable: skip jobs whose target is unmounted/unreachable instead of aborting
    def sync_jobs(cfg, program, jobs, dry_run: false, quiet: false, skip_unavailable: false)
      jobs = jobs.select { |j| j.active == 1 }
      return true if jobs.empty?

      # one availability check per unique target root:
      # local targets must be mounted volumes, remote ones reachable via ssh
      availability = {}
      jobs.each { |j| availability[j.target] ||= target_availability(j) }
      jobs, unavailable = jobs.partition { |j| availability[j.target] == :ok }

      unavailable.map { |j| availability[j.target] }.uniq.each do |reason|
        if skip_unavailable
          puts "skipped: #{reason}" unless quiet
        else
          warn "abort: #{reason}"
          exit 1
        end
      end
      return true if jobs.empty?

      conflicts = jobs.select(&:conflict)
      unless conflicts.empty?
        warn "warning: target is newer than source:"
        conflicts.each { |j| warn "  ! #{j.path}" }
        warn "continuing sync (--update skips newer files on target)."
      end

      header_printed = false
      all_ok = true
      jobs.each do |job|
        success, output, transferred = Twin::Sync.run_job(cfg, job, dry_run: dry_run)
        Twin::Journal.record(job, success: success, transferred: transferred, output: output) unless dry_run
        all_ok &&= success
        next if quiet && success && !transferred

        unless header_printed
          puts "→ #{program.name}"
          header_printed = true
        end
        puts "  • #{job.path}"
        puts output.gsub(/^/, "    ") if output && !output.strip.empty?
        warn "  error syncing #{job.path}" unless success
      end
      all_ok
    end

    # :ok, or a human-readable reason the target can't be synced right now.
    def target_availability(job)
      if job.remote?
        host, = Twin::Remote.split(job.target)
        return :ok if Twin::Remote.reachable?(host)
        "#{host} is not reachable via ssh"
      else
        return :ok if Twin::Sync.mounted?(job.target)
        "#{job.target} is not a mounted volume"
      end
    end

    # ── add ────────────────────────────────────────────────────────────────────

    def cmd_add(cfg, args)
      result = Twin::Add.run(cfg, args)
      return unless result && result[:dry_run]
      cmd_sync(cfg, ["-p", result[:program],
                     "--file=#{File.basename(result[:file])}", "--dry-run"])
    end

    # ── log ────────────────────────────────────────────────────────────────────

    def cmd_log(args)
      n = 20
      json = false
      OptionParser.new do |o|
        o.on("-n N", Integer) { |v| n = v }
        o.on("--json")        { json = true }
      end.parse!(args)

      entries = Twin::Journal.tail(n)
      if json
        puts JSON.pretty_generate(entries)
        return
      end
      if entries.empty?
        puts "journal is empty (#{Twin::Journal.log_path})"
        return
      end

      tty = $stdout.tty?
      entries.each do |e|
        ts   = Time.parse(e["ts"]).strftime("%Y-%m-%d %H:%M:%S")
        mark = e["ok"] ? "✓" : "✗"
        mark = Picker.colorize(e["ok"] ? :in_sync : :both_missing, mark) if tty
        note = e["ok"] ? (e["changed"] ? "changed" : "no-op") : "error: #{e["error"]}"
        puts "#{ts}  #{mark}  #{e["program"]}  #{e["path"]}  (#{note})"
      end
    end

    # ── doctor ─────────────────────────────────────────────────────────────────

    def cmd_doctor(cfg)
      ok = true

      puts "Tools"
      %w[grubber rsync fzf].each do |bin|
        if tool_available?(bin)
          puts "  ✓  #{bin}"
        else
          puts "  ✗  #{bin}  (required — not found in PATH)"
          ok = false
        end
      end

      puts "\nRenderers (preview)"
      found_renderer = false
      %w[apex glow bat].each do |bin|
        if tool_available?(bin)
          puts "  ✓  #{bin}"
          found_renderer = true
        else
          puts "  –  #{bin}  (not installed)"
        end
      end
      puts "  ⚠   no renderer found — file preview will fall back to cat" unless found_renderer

      puts "\nTemplating"
      if cfg.hosts.empty?
        puts "  –  no hosts configured"
      else
        %i[host target].each do |attr|
          name = cfg.send(attr)
          if name.empty?
            puts "  ✗  #{attr} not set in config"
            ok = false
          elsif cfg.hosts.key?(name)
            puts "  ✓  #{attr}: #{name}"
          else
            puts "  ✗  #{attr} #{name.inspect} not found in hosts"
            ok = false
          end
        end
      end

      puts "\nTargets"
      begin
        programs = Scanner.load_programs(cfg, show_all: true)
        puts "  ✓  all template tokens resolved" unless cfg.hosts.empty?
        targets  = programs.flat_map(&:jobs).map(&:target).uniq.sort
        if targets.empty?
          puts "  (no programs loaded)"
        else
          targets.each do |tgt|
            if Twin::Remote.remote?(tgt)
              host, = Twin::Remote.split(tgt)
              if Twin::Remote.reachable?(host)
                puts "  ✓  #{tgt}  (ssh)"
              else
                puts "  ✗  #{tgt}  (ssh: #{host} not reachable)"
                ok = false
              end
            elsif Twin::Sync.mounted?(tgt)
              puts "  ✓  #{tgt}"
            else
              puts "  ✗  #{tgt}  (not mounted)"
              ok = false
            end
          end
        end
      rescue => e
        puts "  ✗  #{e.message}"
        ok = false
      end

      puts ok ? "\nAll checks passed." : "\nSome checks failed."
      exit 1 unless ok
    end

    def tool_available?(name)
      system("command -v #{name} > /dev/null 2>&1")
    end

    # ── option parsing ─────────────────────────────────────────────────────────

    def parse_filter_opts(args)
      opts = { show_all: false, label: nil, file: nil, json: false }
      OptionParser.new do |o|
        o.on("--all")          { opts[:show_all] = true }
        o.on("--label=L")      { |v| opts[:label] = v }
        o.on("--file=F")       { |v| opts[:file] = v }
        o.on("--json")         { opts[:json] = true }
      end.parse!(args)
      opts
    end

    def parse_sync_opts(args)
      opts = { show_all: false, label: nil, file: nil, pattern: nil, dry_run: false,
               quiet: false, skip_unavailable: false }
      OptionParser.new do |o|
        o.on("--all")          { opts[:show_all] = true }
        o.on("--label=L")      { |v| opts[:label] = v }
        o.on("--file=F")       { |v| opts[:file] = v }
        o.on("-p", "--pattern=P") { |v| opts[:pattern] = v }
        o.on("--dry-run")      { opts[:dry_run] = true }
        o.on("-q", "--quiet")  { opts[:quiet] = true }
        o.on("--skip-unavailable") { opts[:skip_unavailable] = true }
      end.parse!(args)
      opts
    end

    def program_to_hash(p)
      {
        name:        p.name,
        status:      p.status,
        sync_file:   p.sync_file,
        label:       p.label,
        description: p.description,
        jobs:        p.jobs.map { |j| job_to_hash(j) },
      }
    end

    def job_to_hash(j)
      h = j.to_h
      h[:status]       = j.status
      h[:source_mtime] = j.source_mtime&.iso8601
      h[:target_mtime] = j.target_mtime&.iso8601
      h
    end
  end
end
