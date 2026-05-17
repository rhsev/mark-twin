require "optparse"
require "json"

require_relative "config"
require_relative "scanner"
require_relative "sync"
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
          conflict = j.conflict ? (tty ? "  #{Picker.colorize(:target_newer, "!")}" : "  !") : ""
          j_icon = tty ? Picker.colorize(j.status, "") : ""
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
        puts "no matching programs"
        return
      end

      programs.each { |p| sync_program(cfg, p, dry_run: opts[:dry_run]) }
    end

    def sync_program(cfg, program, dry_run: false)
      sync_jobs(cfg, program, program.active_jobs, dry_run: dry_run)
    end

    def sync_jobs(cfg, program, jobs, dry_run: false)
      jobs = jobs.select { |j| j.active == 1 }
      return if jobs.empty?

      # one mount check per unique target root
      checked = Set.new
      jobs.each do |j|
        next if checked.include?(j.target)
        unless Twin::Sync.mounted?(j.target)
          warn "abort: #{j.target} is not a mounted volume"
          exit 1
        end
        checked << j.target
      end

      conflicts = jobs.select(&:conflict)
      unless conflicts.empty?
        warn "warning: target is newer than source:"
        conflicts.each { |j| warn "  ! #{j.path}" }
        warn "continuing sync (--update skips newer files on target)."
      end

      puts "→ #{program.name}"
      jobs.each do |job|
        success, output = Twin::Sync.run_job(cfg, job, dry_run: dry_run)
        puts "  • #{job.path}"
        puts output.gsub(/^/, "    ") if output && !output.strip.empty?
        warn "  error syncing #{job.path}" unless success
      end
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
      opts = { show_all: false, label: nil, file: nil, pattern: nil, dry_run: false }
      OptionParser.new do |o|
        o.on("--all")          { opts[:show_all] = true }
        o.on("--label=L")      { |v| opts[:label] = v }
        o.on("--file=F")       { |v| opts[:file] = v }
        o.on("-p", "--pattern=P") { |v| opts[:pattern] = v }
        o.on("--dry-run")      { opts[:dry_run] = true }
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

require "set"
