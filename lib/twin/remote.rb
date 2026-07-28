require "open3"

module Twin
  # Remote (ssh) targets, written exactly as rsync understands them:
  # "user@host:/path" or "host:/path". A target counts as remote when a colon
  # appears before the first slash. Sources stay local — twin pushes.
  module Remote
    module_function

    SSH_OPTS = ["-o", "BatchMode=yes", "-o", "ConnectTimeout=5"].freeze

    def remote?(target)
      %r{\A[^/]+:}.match?(target.to_s)
    end

    # "user@host:/path" → ["user@host", "/path"]
    def split(target)
      host, path = target.to_s.split(":", 2)
      [host, path.to_s]
    end

    # Non-interactive reachability probe (BatchMode: never asks for a password).
    def reachable?(host)
      system("ssh", *SSH_OPTS, host, "true", out: File::NULL, err: File::NULL)
    end

    # Stat many paths in one ssh round-trip. Paths go over stdin (one per
    # line), the remote loop answers "path<TAB>epoch" or "path<TAB>-" for
    # missing ones. Tries BSD stat first, then GNU — covers macOS and Linux.
    # Returns {path => Time or nil-if-missing}, or nil when ssh itself failed.
    STAT_SCRIPT = <<~SH.freeze
      while IFS= read -r p; do
        if [ -e "$p" ]; then
          printf '%s\t%s\n' "$p" "$(stat -f %m -- "$p" 2>/dev/null || stat -c %Y -- "$p")"
        else
          printf '%s\t-\n' "$p"
        fi
      done
    SH

    def stat_paths(host, paths)
      return {} if paths.empty?
      out, _err, status = Open3.capture3(
        "ssh", *SSH_OPTS, host, STAT_SCRIPT,
        stdin_data: paths.join("\n") + "\n"
      )
      return nil unless status.success?

      result = {}
      out.each_line do |line|
        path, mtime = line.chomp.split("\t", 2)
        next unless path && mtime
        result[path] = mtime == "-" ? nil : Time.at(mtime.to_i)
      end
      result
    rescue Errno::ENOENT
      nil # ssh not installed
    end

    # Create a directory on the remote side (mkdir -p equivalent).
    def mkdir_p(host, dir)
      _out, _err, status = Open3.capture3("ssh", *SSH_OPTS, host, "mkdir", "-p", shellesc(dir))
      status.success?
    end

    # Escape one argument for the remote shell (ssh joins args with spaces and
    # hands the string to a shell — local exec-style arrays don't protect it).
    def shellesc(s)
      "'" + s.gsub("'", "'\\\\''") + "'"
    end
  end
end
