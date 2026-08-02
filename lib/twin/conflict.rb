require "digest"

require_relative "remote"

module Twin
  # Finding out which files on the target would be silently skipped by rsync's
  # --update, and whether that actually matters.
  #
  # `Job#conflict` is no help here. It compares the mtime of the job's own path,
  # and for a directory job that is the directory's mtime — which says nothing
  # about the files inside it. Worse, it is wrong in exactly the case that
  # matters: editing a file in place leaves its directory's mtime untouched, and
  # `rsync -a` equalises directory mtimes on every run anyway. A hand-edit on
  # the target is therefore invisible to it.
  #
  # So we ask rsync, which has the answer already and knows its own matching
  # rules better than any reimplementation would:
  #
  #   1. Which files does --update hold back?
  #      Dry-run twice, once with --update and once without. Everything the
  #      second run would transfer but the first would not is exactly the set
  #      --update protects.
  #
  #   2. Of those, which differ in content?
  #      Only these are worth asking about. A file that is merely newer — same
  #      bytes, later timestamp — is noise, and noise is what turns a prompt
  #      into a reflex. Sync both sides of a tree in either order and you get
  #      dozens of them.
  #
  # The first dry-run is also the cheap exit: when a forced run would move
  # nothing, the job is fully in sync and the second run is skipped. That is the
  # common case, so a quiet sync costs one extra stat-walk per job and no more.
  module Conflict
    # One file the target owns more recently than the source, with content that
    # actually differs. `same_content` is nil when it could not be determined
    # (remote targets) — treated as a conflict, because guessing in the other
    # direction would overwrite work.
    Entry = Struct.new(:job, :rel, :source_path, :target_path,
                       :source_mtime, :target_mtime, keyword_init: true) do
      def age_delta
        return nil unless source_mtime && target_mtime
        target_mtime - source_mtime
      end
    end

    # rsync --itemize-changes line → relative path. Change lines start with an
    # update type and a file type (">f.st...... lib/foo.rb"); "*deleting" and
    # the surrounding prose do not match.
    ITEMIZE_LINE = /\A[<>ch][fdLDS]\S*\s+(.+?)\s*\z/

    module_function

    # Real conflicts for one job, in the order rsync reports them.
    # Empty when --update holds nothing back, or holds back only identical files.
    def detect(cfg, job)
      # Render jobs compare by content already and never use --update.
      return [] if job.render
      return [] unless job.source_exists && job.target_exists

      held_back_paths(cfg, job).filter_map { |rel| entry_for(job, rel) }
    end

    # Relative paths that --update would skip: (would transfer forced) minus
    # (would transfer normally).
    def held_back_paths(cfg, job)
      forced = itemized_paths(Twin::Sync.rsync_args(cfg, job, dry_run: true, force: true))
      return [] if forced.empty?   # nothing to move at all — no need to ask rsync twice

      normal = itemized_paths(Twin::Sync.rsync_args(cfg, job, dry_run: true, force: false))
      forced - normal
    end

    def itemized_paths(args)
      output, status = Twin::Sync.run(args)
      return [] unless status.success?
      output.lines.filter_map do |line|
        m = ITEMIZE_LINE.match(line)
        next unless m
        rel = m[1]
        next if rel == "./" || rel.end_with?("/")   # directories carry no content
        rel
      end
    end

    # Build an Entry unless source and target hold the same bytes.
    def entry_for(job, rel)
      src = resolve(job.source_path, rel)
      tgt = resolve(job.target_path, rel)

      # Remote targets can't be read here; report them rather than assume.
      unless job.remote?
        return nil if same_content?(src, tgt)
      end

      Entry.new(
        job: job, rel: rel, source_path: src, target_path: tgt,
        source_mtime: mtime(src), target_mtime: job.remote? ? nil : mtime(tgt),
      )
    end

    # A job path may be a single file — rsync then itemizes its basename, and
    # the job path is already the full path.
    def resolve(base, rel)
      File.directory?(base) ? File.join(base, rel) : base
    end

    def same_content?(a, b)
      return false unless File.file?(a) && File.file?(b)
      return false unless File.size(a) == File.size(b)
      digest(a) == digest(b)
    rescue Errno::ENOENT, Errno::EACCES
      false
    end

    def digest(path) = Digest::SHA256.file(path).hexdigest

    def mtime(path)
      File.mtime(path)
    rescue Errno::ENOENT, Errno::EACCES
      nil
    end

    # Unified diff for one entry, or a short note when it can't be produced.
    def diff(entry)
      return "  (remote target — no diff available)" if entry.job.remote?
      return "  (binary or unreadable)" unless text?(entry.source_path) && text?(entry.target_path)

      out, _status = Twin::Sync.run([
        "diff", "-u",
        "--label", "target (#{entry.target_path})", entry.target_path,
        "--label", "source (#{entry.source_path})", entry.source_path,
      ])
      out.empty? ? "  (no textual difference)" : out
    end

    # Cheap heuristic: a NUL byte in the first 8 KiB means binary.
    def text?(path)
      File.open(path, "rb") { |f| !f.read(8192).to_s.include?("\0") }
    rescue Errno::ENOENT, Errno::EACCES
      false
    end
  end
end
