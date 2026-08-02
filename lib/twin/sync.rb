require "fileutils"

require_relative "remote"

module Twin
  module Sync
    module_function

    # A line from `rsync --itemize-changes` describing a real change: an
    # itemize code whose first column is the update type (< > c h *) and second
    # the file type (f d L D S), e.g. ">f+++++++++", "cd+++++++++", "*deleting".
    # No-op runs emit no such line; headers ("sending …", "created directory …")
    # and the summary don't match. Deterministic — no scraping of prose.
    ITEMIZE_CHANGE = /\A[<>ch*][fdLDS]/

    # True when rsync reported at least one changed item.
    def transferred?(output)
      output.lines.any? { |l| ITEMIZE_CHANGE.match?(l) }
    end

    # True if the path lives on a mounted volume other than the root filesystem.
    # Walks up parents until it finds a mount point (different device than parent)
    # or hits "/" (path is on the root volume, not externally mounted).
    def mounted?(path)
      return false unless File.exist?(path)
      current = File.expand_path(path)
      until current == "/"
        parent = File.expand_path("..", current)
        return true if File.stat(current).dev != File.stat(parent).dev
        current = parent
      end
      false
    rescue Errno::ENOENT
      false
    end

    # Render a template Job: read source, substitute {{vars}}, write if changed.
    # Returns [success, output, changed].
    def render_job(cfg, job, dry_run: false)
      return [false, "render: remote targets are not supported", false] if job.remote?

      src = job.source_path
      tgt = job.target_path

      return [false, "source not found: #{src}", false] unless File.exist?(src)
      return [false, "render: source must be a file, not a directory: #{src}", false] if File.directory?(src)

      begin
        rendered = Twin::Template.render_file(src, cfg.var_map, context: job.path)
      rescue => e
        return [false, e.message, false]
      end

      if dry_run
        return [true, "(dry-run) would render #{File.basename(src)} → #{tgt}", false]
      end

      FileUtils.mkdir_p(File.dirname(tgt))

      current = File.exist?(tgt) ? File.binread(tgt) : nil
      changed = (current != rendered)

      if changed
        File.binwrite(tgt, rendered)
        output = "rendered #{File.basename(src)} → #{tgt}"
      else
        output = "#{File.basename(src)}: content unchanged"
      end

      if !job.cmd.empty?
        if changed
          cmd_out, cmd_status = run(["sh", "-c", job.cmd])
          output += "\ncmd: #{job.cmd}\n#{cmd_out}"
          unless cmd_status.success?
            output += "cmd failed (exit #{cmd_status.exitstatus})"
            return [false, output, changed]
          end
        else
          output += "\ncmd skipped (content unchanged)"
        end
      end

      [true, output, changed]
    end

    # Sync one Job. Returns [success, combined_output, transferred].
    # transferred is true when rsync actually moved bytes (false on no-op or dry_run).
    def run_job(cfg, job, dry_run: false, force: false)
      return render_job(cfg, job, dry_run: dry_run) if job.render

      src = job.source_path
      tgt = job.target_path

      return [false, "source not found: #{src}", false] unless File.exist?(src)

      if job.remote?
        host, rpath = Twin::Remote.split(tgt)
        unless dry_run || Twin::Remote.mkdir_p(host, File.dirname(rpath))
          return [false, "ssh: could not create #{File.dirname(rpath)} on #{host}", false]
        end
      else
        FileUtils.mkdir_p(File.dirname(tgt))
      end

      output, status = run(rsync_args(cfg, job, dry_run: dry_run, force: force))
      return [false, output, false] unless status.success?

      xfr = !dry_run && transferred?(output)

      if job.conflict && !xfr && !dry_run && !force
        output += "\nskipped: target is newer, source not synced"
      end

      if !job.cmd.empty? && !dry_run
        if xfr
          cmd_out, cmd_status = run(["sh", "-c", job.cmd])
          output += "\ncmd: #{job.cmd}\n#{cmd_out}"
          unless cmd_status.success?
            output += "cmd failed (exit #{cmd_status.exitstatus})"
            return [false, output, xfr]
          end
        else
          output += "\ncmd skipped (nothing transferred)"
        end
      end

      [true, output, xfr]
    end

    # Full rsync argument vector for a job.
    #
    # force: drop --update, so a file that is newer on the target is overwritten
    # anyway. Only ever set after the user agreed to it (see Twin::Conflict), or
    # via `twin sync --force`.
    def rsync_args(cfg, job, dry_run: false, force: false)
      src = job.source_path
      tgt = job.target_path

      args = ["rsync", "-av", "--itemize-changes"]
      args << "--update" unless force
      if job.delete
        args << "--delete"
        args.concat(backup_args(job))
      end
      args << "--dry-run" if dry_run
      cfg.global_excludes.each { |ex| args << "--exclude=#{ex}" }
      job.all_excludes.each   { |ex| args << "--exclude=#{ex}" }

      if File.directory?(src)
        args << "#{src}/" << "#{tgt}/"
      else
        args << src << tgt
      end
      args
    end

    # Safety net for --delete: deleted and overwritten files land in a
    # per-run backup dir on the target side (<target>/.twin-backup/<stamp>).
    # rsync only creates the dir when it actually backs something up.
    # The exclude keeps a backup dir inside the transfer root (Path: ".")
    # from being deleted by the very sync it protects against.
    def backup_args(job)
      root = job.remote? ? Twin::Remote.split(job.target).last : job.target
      dir  = File.join(root, ".twin-backup", run_stamp)
      ["--backup", "--backup-dir=#{dir}", "--exclude=.twin-backup/"]
    end

    # One timestamp per twin process, so a multi-job run shares a backup dir.
    def run_stamp
      @run_stamp ||= Time.now.strftime("%Y-%m-%d_%H%M%S")
    end

    # Sync all jobs in a Program. Returns array of [job, success, output].
    def run_program(cfg, program, dry_run: false, force: false)
      program.active_jobs.map { |job| [job, *run_job(cfg, job, dry_run: dry_run, force: force)] }
    end

    def run(args)
      require "open3"
      stdout, stderr, status = Open3.capture3(*args)
      [stdout + stderr, status]
    rescue Errno::ENOENT
      raise "command not found: #{args.first}"
    end
  end
end
