require "yaml"

require_relative "template"

module Twin
  # `twin add <path>` — guided scaffolding of a new sync entry. Turns the
  # judgment calls of the "add a sync" recipe (which sync-file? which Path?
  # what to exclude? deploy hook?) into prompts with sensible defaults, then
  # appends a Markdown block to the chosen sync-file.
  module Add
    module_function

    # Directories that are usually machine-generated or heavy — offered as
    # exclude defaults when present in the source directory.
    SUGGEST_EXCLUDES = %w[.git node_modules .venv __pycache__ dist build target].freeze

    # ── pure helpers (unit-tested) ────────────────────────────────────────────

    # Frontmatter of a sync-file as a Hash (Source/Target token-substituted),
    # or nil when the file has none / it isn't a Hash.
    def frontmatter(file, vars = {})
      text = File.read(file)
      return nil unless text.start_with?("---\n")
      body = text[4..].split(/^---\s*$/, 2).first
      data = begin
        YAML.safe_load(body.to_s)
      rescue Psych::SyntaxError
        nil
      end
      return nil unless data.is_a?(Hash)
      %w[Source Target].each do |k|
        next unless data[k].is_a?(String)
        data[k] = Twin::Template.substitute(data[k], vars, context: File.basename(file))
      end
      data
    rescue Errno::ENOENT
      nil
    end

    # Sync-files in dir whose Source is an ancestor of path.
    # Returns [[file, frontmatter], …].
    def candidates(dir, path, vars = {})
      Dir.glob(File.join(dir, "*.md")).sort.filter_map do |f|
        fm = frontmatter(f, vars)
        next unless fm && fm["Source"].is_a?(String) && !fm["Source"].empty?
        root = File.expand_path(fm["Source"])
        next unless path == root || path.start_with?(root + "/")
        [f, fm]
      end
    end

    def relative_path(root, path)
      path == root ? "." : path[(root.length + 1)..]
    end

    def suggest_excludes(path)
      return [] unless File.directory?(path)
      SUGGEST_EXCLUDES.filter_map do |e|
        full = File.join(path, e)
        next unless File.exist?(full)
        File.directory?(full) ? "#{e}/" : e
      end
    end

    def build_block(program:, path:, description: "", excludes: [], delete: false, cmd: "", prose: "")
      yaml = ["Program: #{program}", "Path: #{path}"]
      yaml << "Description: #{description}"        unless description.empty?
      yaml << "Exclude: #{excludes.join(',')}"     unless excludes.empty?
      yaml << "Delete: true"                       if delete
      yaml << "Cmd: #{cmd}"                        unless cmd.empty?
      prose = "TODO: document why this path is synced." if prose.empty?
      "\n## #{program}\n\n#{prose}\n\n```yaml\n#{yaml.join("\n")}\n```\n"
    end

    def frontmatter_text(source:, target:, label: "")
      lines = ["---", "Active: 1"]
      lines << "Label: #{label}" unless label.empty?
      lines << "Source: #{source}" << "Target: #{target}" << "---" << ""
      lines.join("\n")
    end

    # ── interactive flow ──────────────────────────────────────────────────────

    # Returns {program:, file:, dry_run:} on success (dry_run: whether the
    # user asked for one), nil when nothing was written.
    def run(cfg, args)
      raw = args.first
      raise "usage: twin add <path>" if raw.nil? || raw.empty?
      path = File.expand_path(raw)
      raise "not found: #{path}" unless File.exist?(path)

      vars = cfg.var_map
      picked = pick_sync_file(cfg, path, vars)
      return nil unless picked
      file, fm = picked

      root = File.expand_path(fm["Source"])
      rel  = relative_path(root, path)

      if File.exist?(file) && File.read(file).match?(/^Path:\s*#{Regexp.escape(rel)}\s*$/)
        raise "#{File.basename(file)} already has a block with Path: #{rel}"
      end

      program = ask("Program name", File.basename(path))
      prose   = ask("Why is this synced? (one line of prose)")
      desc    = ask("Description (short, for listings)", program)
      excl    = ask("Exclude (comma-separated)", suggest_excludes(path).join(","))
                  .split(",").map(&:strip).reject(&:empty?)
      delete  = yes?(ask("Mirror deletions on target (Delete: true)? (y/N)", "n"))
      cmd     = ask("Post-sync Cmd (empty for none)")

      block = build_block(program: program, path: rel, description: desc,
                          excludes: excl, delete: delete, cmd: cmd, prose: prose)
      File.open(file, "a") { |f| f.write(block) }

      puts "\nadded #{program.inspect} to #{File.basename(file)}"
      puts "  #{File.join(root, rel)}  →  #{File.join(fm['Target'].to_s, rel)}"

      dry = yes?(ask("Run a dry-run now? (Y/n)", "y"))
      { program: program, file: file, dry_run: dry }
    end

    # Choose (or create) the sync-file covering path.
    # Returns [file, frontmatter] or nil.
    def pick_sync_file(cfg, path, vars)
      cands = candidates(cfg.sync_dir, path, vars)
      case cands.size
      when 0 then offer_new_sync_file(cfg, path, vars)
      when 1
        file, fm = cands.first
        puts "sync-file: #{File.basename(file)}  (#{fm['Source']} → #{fm['Target']})"
        cands.first
      else
        puts "multiple sync-files cover #{path}:"
        cands.each_with_index do |(f, fm), i|
          puts "  #{i + 1}) #{File.basename(f)}  (#{fm['Source']} → #{fm['Target']})"
        end
        n = ask("Which one?", "1").to_i
        cands[n - 1] or raise "invalid choice: #{n}"
      end
    end

    def offer_new_sync_file(cfg, path, vars)
      puts "no sync-file in #{cfg.sync_dir} covers #{path}"
      return nil unless yes?(ask("Create a new sync-file? (y/N)", "n"))

      name = ask("File name", "#{File.basename(path).sub(/\A\./, '')}.md")
      name += ".md" unless name.end_with?(".md")
      file = File.join(cfg.sync_dir, name)
      raise "already exists: #{file}" if File.exist?(file)

      source = ask("Source base on this machine", File.dirname(path))
      target = ask("Target base (mount path or user@host:/path)")
      raise "Target is required" if target.empty?
      label  = ask("Label (e.g. mini → server)")

      File.write(file, frontmatter_text(source: source, target: target, label: label))
      puts "created #{File.basename(file)}"
      fm = { "Source" => source, "Target" => target }
      %w[Source Target].each do |k|
        fm[k] = Twin::Template.substitute(fm[k], vars, context: name)
      end
      [file, fm]
    end

    def ask(prompt, default = "")
      print default.empty? ? "#{prompt}: " : "#{prompt} [#{default}]: "
      $stdout.flush
      ans = $stdin.gets
      raise "aborted (stdin closed)" if ans.nil?
      ans = ans.strip
      ans.empty? ? default : ans
    end

    def yes?(answer) = answer.match?(/\Ay/i)
  end
end
