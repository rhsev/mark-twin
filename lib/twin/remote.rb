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
    # missing ones. Tries BSD stat, then GNU, then BusyBox `date -r` — covers
    # macOS, Linux and OpenWrt. Auf dem Brume (25.08.2026) fehlte `stat`
    # komplett: die leere Substitution wurde als Epoche 0 geparst und `twin
    # status` zeigte 1970 statt "unbekannt". `date -r` kennt kein `--`;
    # verkraftbar, weil hier nur absolute Zielpfade ankommen.
    # Returns {path => Time or nil-if-missing}, or nil when ssh itself failed.
    # POSIX sh, and it must stay free of single quotes: it is handed to the
    # remote side wrapped in '...' so that *any* login shell passes it through
    # literally. The usual POSIX escape for an embedded quote ('\'') is parsed
    # differently by fish, so the rule here is simply not to need it — hence
    # double quotes around the printf formats.
    STAT_SCRIPT = <<~SH.freeze
      while IFS= read -r p; do
        if [ -e "$p" ]; then
          m=$(stat -f %m -- "$p" 2>/dev/null || stat -c %Y -- "$p" 2>/dev/null || date -r "$p" +%s)
          printf "%s\t%s\n" "$p" "$m"
        else
          printf "%s\t-\n" "$p"
        fi
      done
    SH

    def stat_paths(host, paths)
      return {} if paths.empty?
      # Explicitly through /bin/sh. ssh hands the command to the *login shell*
      # on the far side, and that is not necessarily POSIX: on a machine whose
      # shell is fish, the bare script dies on "while IFS= read -r p; do" and
      # twin read the failure as "host not answering" — every ssh target showed
      # as :unreachable in status and the picker, for as long as SSH targets
      # existed (#8, 2026-07-17). Found and fixed 2026-08-25.
      raise "STAT_SCRIPT must not contain single quotes" if STAT_SCRIPT.include?("'")

      out, _err, status = Open3.capture3(
        "ssh", *SSH_OPTS, host, "/bin/sh -c '#{STAT_SCRIPT}'",
        stdin_data: paths.join("\n") + "\n"
      )
      return nil unless status.success?

      result = {}
      out.each_line do |line|
        path, mtime = line.chomp.split("\t", 2)
        next unless path && mtime
        # Nur echte Epochen als Zeit werten. Scheitert die ganze stat-Kette,
        # ist das Feld leer — das ist "unbekannt", nicht 1970.
        result[path] = mtime.match?(/\A\d+\z/) ? Time.at(mtime.to_i) : nil
      end
      result
    rescue Errno::ENOENT
      nil # ssh not installed
    end

    # Checksum many paths in one ssh round-trip, same shape as stat_paths:
    # paths over stdin, "path<TAB>md5" back, "-" for anything that is not a
    # regular file. Tries BSD md5 first, then md5sum (GNU, BusyBox) — covers
    # macOS, Linux and OpenWrt. md5sum prints "hash  path"; the parameter
    # expansion keeps only the first word. Same single-quote rule as
    # STAT_SCRIPT, and MD5 is drift detection here, not cryptography.
    # Returns {path => hex or nil-if-unreadable}, or nil when ssh itself failed.
    MD5_SCRIPT = <<~SH.freeze
      while IFS= read -r p; do
        if [ -f "$p" ]; then
          m=$(md5 -q "$p" 2>/dev/null || md5sum "$p" 2>/dev/null)
          m=${m%% *}
          printf "%s\t%s\n" "$p" "$m"
        else
          printf "%s\t-\n" "$p"
        fi
      done
    SH

    def md5_paths(host, paths)
      return {} if paths.empty?
      raise "MD5_SCRIPT must not contain single quotes" if MD5_SCRIPT.include?("'")

      out, _err, status = Open3.capture3(
        "ssh", *SSH_OPTS, host, "/bin/sh -c '#{MD5_SCRIPT}'",
        stdin_data: paths.join("\n") + "\n"
      )
      return nil unless status.success?

      result = {}
      out.each_line do |line|
        path, sum = line.chomp.split("\t", 2)
        next unless path && sum
        result[path] = sum.match?(/\A\h{32}\z/) ? sum.downcase : nil
      end
      result
    rescue Errno::ENOENT
      nil # ssh not installed
    end

    # Which of the tools twin relies on exist on the far side? One ssh
    # round-trip per host, for `twin doctor`. rsync carries the sync itself;
    # stat/date feed the batched mtime round; md5/md5sum feed the content
    # check. Same single-quote rule as the other batch scripts.
    # Returns {tool => present?}, or nil when ssh itself failed.
    PREFLIGHT_SCRIPT = <<~SH.freeze
      for t in rsync stat date md5 md5sum; do
        if command -v "$t" >/dev/null 2>&1; then
          printf "%s\tok\n" "$t"
        else
          printf "%s\t-\n" "$t"
        fi
      done
    SH

    def preflight(host)
      raise "PREFLIGHT_SCRIPT must not contain single quotes" if PREFLIGHT_SCRIPT.include?("'")

      out, _err, status = Open3.capture3("ssh", *SSH_OPTS, host, "/bin/sh -c '#{PREFLIGHT_SCRIPT}'")
      return nil unless status.success?
      parse_preflight(out)
    rescue Errno::ENOENT
      nil # ssh not installed
    end

    def parse_preflight(out)
      result = {}
      out.each_line do |line|
        tool, state = line.chomp.split("\t", 2)
        next unless tool && state
        result[tool] = state == "ok"
      end
      result
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
