require "fileutils"

module Twin
  module Sync
    module_function

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

    # Sync one Job. Returns [success, combined_output].
    def run_job(cfg, job, dry_run: false)
      src = job.source_path
      tgt = job.target_path

      return [false, "source not found: #{src}"] unless File.exist?(src)

      FileUtils.mkdir_p(File.dirname(tgt))

      args = ["rsync", "-av", "--update"]
      args << "--dry-run" if dry_run
      cfg.global_excludes.each { |ex| args << "--exclude=#{ex}" }
      job.excludes.each       { |ex| args << "--exclude=#{ex}" }

      if File.directory?(src)
        args << "#{src}/" << "#{tgt}/"
      else
        args << src << tgt
      end

      output, status = run(args)
      return [false, output] unless status.success?

      if !job.cmd.empty? && !dry_run
        cmd_out, cmd_status = run(["sh", "-c", job.cmd])
        output += "\ncmd: #{job.cmd}\n#{cmd_out}"
        return [false, output] unless cmd_status.success?
      end

      [true, output]
    end

    # Sync all jobs in a Program. Returns array of [job, success, output].
    def run_program(cfg, program, dry_run: false)
      program.active_jobs.map { |job| [job, *run_job(cfg, job, dry_run: dry_run)] }
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
