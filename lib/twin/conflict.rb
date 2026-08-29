require "digest"
require "set"

require_relative "remote"

module Twin
  # What would a sync actually change? mtimes cannot answer that — a
  # directory's mtime records the last entry added or removed (after a sync:
  # the sync itself), a file's mtime moves on a `cat >` copy that changed
  # nothing, and rsync equalises directory mtimes on every run anyway. So we
  # ask rsync, which has the answer already and knows its own matching rules
  # better than any reimplementation would:
  #
  #   1. What would a *forced* run transfer?
  #      One dry-run without --update. Empty means fully in sync — the common
  #      case and the cheap exit: one stat-walk per job and no more.
  #
  #   2. Of that, what does --update hold back?
  #      A second dry-run with --update. Everything the forced run would move
  #      but the normal one would not is exactly the set --update protects —
  #      files the target owns more recently.
  #
  #   3. What differs only in timestamp?
  #      rsync's itemize flags say so (">f..t" — time, same size). Those files
  #      are checksummed; identical content is noise, not drift, and noise is
  #      what turns a prompt into a reflex. Sync both sides of a tree in
  #      either order and you get dozens of them.
  module Conflict
    # The classified outcome of a forced dry-run for one job.
    #   pending   — relative paths a sync would genuinely change
    #   time_only — same bytes, different timestamp; a sync merely aligns them
    #   conflicts — Entry list: target-side changes whose content differs
    Drift = Struct.new(:pending, :time_only, :conflicts, keyword_init: true) do
      def in_sync? = pending.empty? && conflicts.empty?
    end

    # One file the target owns more recently than the source, with content
    # that actually differs (or could not be checked — remote md5 failed —
    # which is treated as differing, because guessing the other way would
    # overwrite work). target_mtime is nil for remote targets.
    Entry = Struct.new(:job, :rel, :source_path, :target_path,
                       :source_mtime, :target_mtime, keyword_init: true) do
      def age_delta
        return nil unless source_mtime && target_mtime
        target_mtime - source_mtime
      end
    end

    # rsync --itemize-changes line → [flags, relative path]. Change lines
    # start with an update type and a file type (">f.st...... lib/foo.rb") or
    # "*deleting"; attribute-only lines (leading ".") and the surrounding
    # prose do not match — they describe no change a sync would make.
    ENTRY_LINE = /\A(\*deleting|[<>ch][fdLDS]\S*)\s+(.+?)\s*\z/

    module_function

    # Classify one job's drift by asking rsync. nil for render jobs (they
    # compare content already and never use --update) and when a side is
    # missing (status reports that on its own).
    def drift(cfg, job)
      return nil if job.render
      return nil unless job.source_exists && job.target_exists

      forced = itemized(Twin::Sync.rsync_args(cfg, job, dry_run: true, force: true))
      return Drift.new(pending: [], time_only: [], conflicts: []) if forced.empty?

      normal_rels = itemized(Twin::Sync.rsync_args(cfg, job, dry_run: true, force: false))
                    .map { |e| e[:rel] }.to_set
      equal = equality_map(job, forced.filter_map { |e| e[:rel] if e[:kind] == :time })

      assemble(forced, normal_rels, equal) do |rel|
        conflict_entry(job, rel)
      end
    end

    # Target-side changes worth asking about, in the order rsync reports them.
    # Empty when --update holds nothing back, or holds back only identical files.
    def detect(cfg, job)
      drift(cfg, job)&.conflicts || []
    end

    # Pure assembly of a Drift from parsed entries. A file the normal run
    # would also transfer flows source→target as intended; one only the
    # forced run would touch is being held back by --update — the target owns
    # it more recently. Content decides whether that is a conflict or noise.
    def assemble(forced, normal_rels, equal)
      d = Drift.new(pending: [], time_only: [], conflicts: [])
      forced.each do |e|
        rel = e[:rel]
        case e[:kind]
        when :deleted, :new then d.pending << rel
        when :content
          normal_rels.include?(rel) ? d.pending << rel : d.conflicts << yield(rel)
        when :time
          if equal[rel]
            d.time_only << rel
          elsif normal_rels.include?(rel)
            d.pending << rel
          else
            d.conflicts << yield(rel)
          end
        end
      end
      d
    end

    # Run rsync and parse its itemize output into [{rel:, kind:}, ...].
    def itemized(args)
      output, status = Twin::Sync.run(args)
      return [] unless status.success?
      output.lines.filter_map do |line|
        m = ENTRY_LINE.match(line)
        next unless m
        kind = classify(m[1])
        next if kind == :attrs
        { rel: m[2], kind: kind }
      end
    end

    # Itemize flags → what kind of change this is.
    #   :deleted — target-only file, removed by Delete: true
    #   :new     — does not exist on the target yet (files and directories)
    #   :content — size differs, so the bytes certainly do
    #   :time    — timestamp only; content equality still to be determined
    #   :attrs   — permissions/owner, no change a sync-file cares about
    def classify(flags)
      return :deleted if flags == "*deleting"
      body = flags[2..].to_s
      return :new     if body.include?("+")
      return :content if body.include?("s")
      return :time    if body.include?("t") || body.include?("T")
      :attrs
    end

    # Content equality for the :time candidates, {rel => bool}. Local pairs
    # are compared directly; remote targets get one batched md5 round per job
    # (STAT_SCRIPT-style), and an unanswered path counts as differing.
    def equality_map(job, rels)
      return {} if rels.empty?
      if job.remote?
        _host, rbase = Twin::Remote.split(job.target_path)
        remote_paths = rels.to_h { |r| [r, job.directory ? File.join(rbase, r) : rbase] }
        sums = Twin::Remote.md5_paths(Twin::Remote.split(job.target)[0], remote_paths.values) || {}
        rels.to_h do |r|
          local = local_md5(resolve(job.source_path, r))
          [r, !local.nil? && sums[remote_paths[r]] == local]
        end
      else
        rels.to_h { |r| [r, same_content?(resolve(job.source_path, r), resolve(job.target_path, r))] }
      end
    end

    def conflict_entry(job, rel)
      src = resolve(job.source_path, rel)
      tgt = job.remote? ? job.target_path : resolve(job.target_path, rel)
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

    def local_md5(path)
      Digest::MD5.file(path).hexdigest
    rescue Errno::ENOENT, Errno::EACCES, Errno::EISDIR
      nil
    end

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
