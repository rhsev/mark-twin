$LOAD_PATH.unshift File.expand_path("../lib", __dir__)

require "minitest/autorun"
require "tempfile"
require "tmpdir"
require "twin"

class TestJobStatus < Minitest::Test
  def job(**kw)
    defaults = {
      program: "p", path: "a", description: "", active: 1, excludes: [],
      label: "", source: "/src", target: "/tgt", cmd: "", sync_file: "",
      source_exists: true, target_exists: true,
      source_mtime: nil, target_mtime: nil, conflict: false,
    }
    Twin::Job.new(**defaults.merge(kw))
  end

  def test_disabled
    assert_equal :disabled, job(active: 0).status
  end

  def test_both_missing
    assert_equal :both_missing, job(source_exists: false, target_exists: false).status
  end

  def test_missing_source
    assert_equal :missing_source, job(source_exists: false).status
  end

  def test_missing_target
    assert_equal :missing_target, job(target_exists: false).status
  end

  def test_conflict
    assert_equal :target_newer, job(conflict: true).status
  end

  def test_in_sync_exact
    now = Time.now
    assert_equal :in_sync, job(source_mtime: now, target_mtime: now).status
  end

  def test_in_sync_within_60s
    now = Time.now
    assert_equal :in_sync, job(source_mtime: now, target_mtime: now - 30).status
  end

  def test_source_newer
    now = Time.now
    assert_equal :source_newer, job(source_mtime: now, target_mtime: now - 3600).status
  end

  def test_target_newer
    now = Time.now
    assert_equal :target_newer, job(source_mtime: now - 3600, target_mtime: now).status
  end

  def test_content_equal_overrides_mtime_drift
    now = Time.now
    assert_equal :in_sync,
                 job(source_mtime: now - 3600, target_mtime: now, content_equal: true).status
  end

  # Directory jobs carry no mtime verdict — without a drift result the honest
  # answer is "not checked", never a guess from directory mtimes.
  def test_directory_without_drift_is_unverified
    now = Time.now
    assert_equal :unverified,
                 job(directory: true, source_mtime: now, target_mtime: now - 3600).status
  end

  def drift(pending: [], time_only: [], conflicts: [])
    Twin::Conflict::Drift.new(pending: pending, time_only: time_only, conflicts: conflicts)
  end

  def test_directory_drift_in_sync
    assert_equal :in_sync, job(directory: true, drift: drift).status
  end

  def test_directory_drift_time_only_is_in_sync
    assert_equal :in_sync, job(directory: true, drift: drift(time_only: ["a"])).status
  end

  def test_directory_drift_pending_is_source_newer
    assert_equal :source_newer, job(directory: true, drift: drift(pending: ["a"])).status
  end

  def test_directory_drift_conflict_wins
    d = drift(pending: ["a"], conflicts: [:entry])
    assert_equal :target_newer, job(directory: true, drift: d).status
  end

  def test_directory_missing_target_reported_before_drift
    assert_equal :missing_target, job(directory: true, target_exists: false).status
  end
end

class TestProgramAggregation < Minitest::Test
  def job(status_args)
    Twin::Job.new(
      program: "X", path: status_args[:path] || "a", description: "", active: 1,
      excludes: [], label: "", source: "/s", target: "/t", cmd: "", sync_file: "f.md",
      source_exists: status_args.fetch(:src, true),
      target_exists: status_args.fetch(:tgt, true),
      source_mtime: status_args[:sm], target_mtime: status_args[:tm],
      conflict: status_args.fetch(:conflict, false),
    )
  end

  def test_status_picks_worst
    now = Time.now
    in_sync = job(sm: now, tm: now)
    missing = job(src: false)
    p = Twin::Program.new(name: "X", jobs: [in_sync, missing])
    assert_equal :missing_source, p.status
  end

  def test_status_all_in_sync
    now = Time.now
    p = Twin::Program.new(name: "X", jobs: [job(sm: now, tm: now), job(sm: now, tm: now)])
    assert_equal :in_sync, p.status
  end

  def test_active_jobs_filter
    active   = job(sm: Time.now, tm: Time.now)
    inactive = Twin::Job.new(**active.to_h.merge(active: 0))
    p = Twin::Program.new(name: "X", jobs: [active, inactive])
    assert_equal 1, p.active_jobs.size
  end

  # :unverified must rank in the aggregation — a forgotten entry would make
  # `find` miss and the fallback report :in_sync, a false green.
  def test_unverified_aggregates_below_problems_above_in_sync
    now = Time.now
    unverified = Twin::Job.new(**job(sm: nil, tm: nil).to_h.merge(directory: true))
    assert_equal :unverified,
                 Twin::Program.new(name: "X", jobs: [job(sm: now, tm: now), unverified]).status
    assert_equal :missing_source,
                 Twin::Program.new(name: "X", jobs: [unverified, job(src: false)]).status
  end
end

class TestScannerGrouping < Minitest::Test
  def make(program, path, sync_file: "f.md")
    Twin::Job.new(
      program: program, path: path, description: "", active: 1, excludes: [],
      label: "", source: "/s", target: "/t", cmd: "", sync_file: sync_file,
      source_exists: true, target_exists: true,
      source_mtime: nil, target_mtime: nil, conflict: false,
    )
  end

  def test_group_by_program_within_file
    jobs = [make("A", "x"), make("B", "y"), make("A", "z")]
    programs = Twin::Scanner.group(jobs)
    assert_equal 2, programs.size
    a = programs.find { |p| p.name == "A" }
    assert_equal 2, a.jobs.size
  end

  def test_same_program_in_different_files_stays_separate
    jobs = [
      make("grubber", ".config/grubber", sync_file: "home.md"),
      make("grubber", "rhsev/grubber",   sync_file: "repos.md"),
    ]
    programs = Twin::Scanner.group(jobs)
    assert_equal 2, programs.size
    assert_equal ["grubber", "grubber"], programs.map(&:name)
    assert_equal ["home.md", "repos.md"], programs.map(&:sync_file).sort
  end

  # Sync-files depend on this: a Cmd that restarts a service goes in the last
  # block so it fires once every path is in place. Regrouping the jobs would
  # restart against half-written state and report success while doing it.
  def test_job_order_follows_document_order
    jobs = [
      make("dylan", "server.rb"), make("dylan", "lib"),
      make("other", "elsewhere"),
      make("dylan", "plugins"),   make("dylan", "config"),
    ]
    dylan = Twin::Scanner.group(jobs).find { |p| p.name == "dylan" }
    assert_equal %w[server.rb lib plugins config], dylan.jobs.map(&:path)
    assert_equal %w[server.rb lib plugins config], dylan.active_jobs.map(&:path)
  end

  def test_program_order_follows_first_appearance
    jobs = [make("B", "x"), make("A", "y"), make("B", "z")]
    assert_equal %w[B A], Twin::Scanner.group(jobs).map(&:name)
  end
end

class TestPickerDelta < Minitest::Test
  def test_in_sync
    now = Time.now
    assert_equal "in sync", Twin::Picker.format_delta(now, now)
  end

  def test_src_minutes
    now = Time.now
    assert_equal "src +5m", Twin::Picker.format_delta(now, now - 300)
  end

  def test_src_hours
    now = Time.now
    assert_equal "src +3h", Twin::Picker.format_delta(now, now - 3 * 3600)
  end

  def test_tgt_days
    now = Time.now
    assert_equal "tgt +2d", Twin::Picker.format_delta(now - 2 * 86400, now)
  end

  def test_empty_on_nil
    assert_equal "", Twin::Picker.format_delta(nil, nil)
  end
end

class TestScannerBuildJob < Minitest::Test
  def valid
    {
      "Program" => "foo", "Path" => ".config/foo",
      "Source" => "/src", "Target" => "/tgt",
      "Description" => "desc", "Active" => 1,
      "Exclude" => "", "Cmd" => "", "Label" => "",
      "_note_file" => "home.md",
    }
  end

  def test_valid_record
    job = Twin::Scanner.build_job(valid)
    refute_nil job
    assert_equal "foo", job.program
    assert_equal ".config/foo", job.path
  end

  def test_missing_path_returns_nil
    assert_nil Twin::Scanner.build_job(valid.merge("Path" => ""))
  end

  def test_missing_source_returns_nil
    assert_nil Twin::Scanner.build_job(valid.merge("Source" => ""))
  end

  def test_missing_target_returns_nil
    assert_nil Twin::Scanner.build_job(valid.merge("Target" => ""))
  end

  def test_excludes_parsed
    job = Twin::Scanner.build_job(valid.merge("Exclude" => "*.tmp, .git"))
    assert_equal ["*.tmp", ".git"], job.excludes
  end

  def test_active_defaults_to_zero
    job = Twin::Scanner.build_job(valid.merge("Active" => nil))
    assert_equal 0, job.active
  end

  def test_cmd_preserved
    job = Twin::Scanner.build_job(valid.merge("Cmd" => "curl http://mi.lan/reload"))
    assert_equal "curl http://mi.lan/reload", job.cmd
  end

  def file_job_for(target_offset, src_content: "x", tgt_content: "y")
    Dir.mktmpdir do |dir|
      src = File.join(dir, "src")
      tgt = File.join(dir, "tgt")
      FileUtils.mkdir_p(src)
      FileUtils.mkdir_p(tgt)
      now = Time.now
      File.write(File.join(src, "a"), src_content)
      File.write(File.join(tgt, "a"), tgt_content)
      File.utime(now, now, File.join(src, "a"))
      File.utime(now + target_offset, now + target_offset, File.join(tgt, "a"))
      return Twin::Scanner.build_job(valid.merge("Path" => "a", "Source" => src, "Target" => tgt))
    end
  end

  def test_conflict_within_tolerance_not_flagged
    refute file_job_for(30).conflict
  end

  def test_conflict_beyond_tolerance_flagged
    assert file_job_for(120).conflict
  end

  # The six false alarms of 2026-08-25: a newer timestamp over
  # identical bytes is a hand-copy, not a conflict.
  def test_identical_content_is_never_a_conflict
    j = file_job_for(120, tgt_content: "x")
    refute j.conflict
    assert j.content_equal
    assert_equal :in_sync, j.status
  end

  def test_directory_job_never_conflicts_on_mtime
    Dir.mktmpdir do |dir|
      src = File.join(dir, "src", "d")
      tgt = File.join(dir, "tgt", "d")
      FileUtils.mkdir_p(src)
      FileUtils.mkdir_p(tgt)
      now = Time.now
      File.utime(now - 3600, now - 3600, src)
      File.utime(now, now, tgt)   # target dir "newer", as after every sync
      j = Twin::Scanner.build_job(valid.merge("Path" => "d",
                                              "Source" => File.dirname(src),
                                              "Target" => File.dirname(tgt)))
      assert j.directory
      refute j.conflict
      assert_equal :unverified, j.status
    end
  end
end

class TestScannerResolveFileArg < Minitest::Test
  def cfg
    Twin::Config.new("sync_dir" => "/sync")
  end

  def test_nil_returns_nils
    scan, filter = Twin::Scanner.resolve_file_arg(cfg, nil)
    assert_nil scan
    assert_nil filter
  end

  def test_empty_returns_nils
    scan, filter = Twin::Scanner.resolve_file_arg(cfg, "")
    assert_nil scan
    assert_nil filter
  end

  def test_bare_name_returns_filter_only
    scan, filter = Twin::Scanner.resolve_file_arg(cfg, "home.md")
    assert_nil scan
    assert_equal "home.md", filter
  end

  def test_absolute_dir
    Dir.mktmpdir do |dir|
      scan, filter = Twin::Scanner.resolve_file_arg(cfg, dir)
      assert_equal dir, scan
      assert_nil filter
    end
  end

  def test_absolute_file
    Tempfile.create(["twin-", ".md"]) do |f|
      scan, filter = Twin::Scanner.resolve_file_arg(cfg, f.path)
      assert_equal File.dirname(f.path), scan
      assert_equal File.basename(f.path), filter
    end
  end

  def test_absolute_missing_raises
    assert_raises(RuntimeError) { Twin::Scanner.resolve_file_arg(cfg, "/no/such/path.md") }
  end
end

class TestPreview < Minitest::Test
  def with_md(content)
    f = Tempfile.new(["twin-test-", ".md"])
    f.write(content)
    f.close
    yield f.path
  ensure
    f&.unlink
  end

  def test_split_frontmatter
    fm, body = Twin::Preview.split_frontmatter("---\nActive: 1\n---\nbody\n")
    assert_includes fm, "Active: 1"
    assert_equal "body\n", body
  end

  def test_split_frontmatter_none
    fm, body = Twin::Preview.split_frontmatter("no frontmatter")
    assert_equal "", fm
    assert_equal "no frontmatter", body
  end

  def test_extract_intro_stops_at_h2
    lines = ["intro", "more", "## Section", "body"]
    assert_equal "intro\nmore", Twin::Preview.extract_intro(lines)
  end

  def test_extract_intro_no_h2
    assert_equal "only", Twin::Preview.extract_intro(["only"])
  end

  def test_find_block_match
    lines = ["```yaml", "Program: foo", "Path: .config/foo", "```"]
    start, stop = Twin::Preview.find_block(lines, ".config/foo")
    assert_equal 0, start
    assert_equal 3, stop
  end

  def test_find_block_no_match
    lines = ["```yaml", "Path: other", "```"]
    assert_nil Twin::Preview.find_block(lines, ".config/foo")
  end

  def test_find_block_quoted_path
    lines = ["```yaml", "Path: '.config/foo'", "```"]
    start, _ = Twin::Preview.find_block(lines, ".config/foo")
    refute_nil start
  end

  def test_extract_compact_includes_section
    md = <<~MD
      ---
      Active: 1
      ---
      Intro text.

      ## Section A

      ```yaml
      Program: foo
      Path: .config/foo
      ```
    MD
    with_md(md) do |path|
      result = Twin::Preview.extract_compact(path, ".config/foo")
      assert_includes result, "Active: 1"
      assert_includes result, "Section A"
      assert_includes result, "Path: .config/foo"
      assert_includes result, "Intro text."
    end
  end

  def test_extract_compact_missing_file_returns_empty
    assert_equal "", Twin::Preview.extract_compact("/no/such/file.md", "foo")
  end

  def test_extract_compact_no_matching_block
    md = "---\nActive: 1\n---\n## A\n\n```yaml\nPath: other\n```\n"
    with_md(md) do |path|
      result = Twin::Preview.extract_compact(path, "nonexistent")
      assert_includes result, "Active: 1"
      refute_includes result, "other"
    end
  end
end

class TestSyncRunJob < Minitest::Test
  def cfg
    Twin::Config.new("sync_dir" => "/tmp")
  end

  def job(**kw)
    defaults = {
      program: "p", path: "a.txt", description: "", active: 1, excludes: [],
      label: "", source: "/nonexistent/src", target: "/nonexistent/tgt", cmd: "",
      sync_file: "", source_exists: false, target_exists: false,
      source_mtime: nil, target_mtime: nil, conflict: false,
    }
    Twin::Job.new(**defaults.merge(kw))
  end

  def test_source_not_found_returns_error
    success, msg = Twin::Sync.run_job(cfg, job)
    refute success
    assert_includes msg, "source not found"
  end

  # Real `rsync -avi` output (itemize-changes).
  NOOP = <<~OUT
    sending incremental file list

    sent 122 bytes  received 13 bytes  270.00 bytes/sec
    total size is 5  speedup is 0.04
  OUT

  XFER = <<~OUT
    sending incremental file list
    >f+++++++++ a.txt
    cd+++++++++ sub/

    sent 228 bytes  received 66 bytes  588.00 bytes/sec
    total size is 5  speedup is 0.02
  OUT

  DELETE = <<~OUT
    sending incremental file list
    *deleting   sub/b.txt

    sent 103 bytes  received 33 bytes  272.00 bytes/sec
    total size is 3  speedup is 0.02
  OUT

  def test_transferred_false_on_noop
    refute Twin::Sync.transferred?(NOOP)
  end

  def test_transferred_true_on_file_and_dir
    assert Twin::Sync.transferred?(XFER)
  end

  def test_transferred_true_on_delete
    assert Twin::Sync.transferred?(DELETE)
  end
end

class TestConfigErrors < Minitest::Test
  def test_invalid_yaml_raises
    f = Tempfile.new(["twin-cfg-", ".yaml"])
    f.write("key: [unclosed\n")
    f.close
    ENV["TWIN_CONFIG"] = f.path
    err = assert_raises(RuntimeError) { Twin::Config.load }
    assert_includes err.message, "config syntax error"
  ensure
    ENV.delete("TWIN_CONFIG")
    f&.unlink
  end

  def test_missing_sync_dir_raises
    err = assert_raises(RuntimeError) { Twin::Config.new("sync_dir" => "/no/such/dir").validate! }
    assert_includes err.message, "sync_dir not found"
  end
end

class TestConfig < Minitest::Test
  def test_defaults
    cfg = Twin::Config.new
    assert_equal [".DS_Store"], cfg.global_excludes
  end

  def test_data_overrides_defaults
    cfg = Twin::Config.new("sync_dir" => "/x", "global_excludes" => ["foo"])
    assert_equal "/x", cfg.sync_dir
    assert_equal ["foo"], cfg.global_excludes
  end

  def test_env_overrides_sync_dir
    ENV["TWIN_SYNC_DIR"] = "/y"
    cfg = Twin::Config.new("sync_dir" => "/x")
    assert_equal "/y", cfg.sync_dir
  ensure
    ENV.delete("TWIN_SYNC_DIR")
  end
end

class TestTemplate < Minitest::Test
  def vars
    { "src.home" => "/Volumes/src", "dst.home" => "/Users/ralf", "dst.mount" => "/Volumes/ralf" }
  end

  def test_passthrough_no_tokens
    assert_equal "plain string", Twin::Template.substitute("plain string", vars, context: "test")
  end

  def test_substitutes_known_token
    result = Twin::Template.substitute("{{src.home}}/foo", vars, context: "test")
    assert_equal "/Volumes/src/foo", result
  end

  def test_substitutes_multiple_tokens
    result = Twin::Template.substitute("{{dst.mount}}/x and {{dst.home}}/y", vars, context: "test")
    assert_equal "/Volumes/ralf/x and /Users/ralf/y", result
  end

  def test_raises_on_unknown_token
    err = assert_raises(RuntimeError) { Twin::Template.substitute("{{unknown}}", vars, context: "ctx") }
    assert_includes err.message, "{{unknown}}"
    assert_includes err.message, "ctx"
  end

  def test_non_string_returned_unchanged
    assert_equal 42, Twin::Template.substitute(42, vars, context: "test")
  end

  def test_substitute_record_replaces_template_fields
    r = { "Source" => "{{src.home}}/sync", "Target" => "{{dst.mount}}", "Path" => "foo" }
    result = Twin::Template.substitute_record(r, vars, context: "test")
    assert_equal "/Volumes/src/sync", result["Source"]
    assert_equal "/Volumes/ralf",     result["Target"]
    assert_equal "foo",               result["Path"]
  end

  def test_substitute_record_noop_without_tokens
    r = { "Source" => "/abs/path", "Target" => "/other" }
    assert_equal r, Twin::Template.substitute_record(r, vars, context: "test")
  end

  def test_substitute_record_noop_with_empty_vars
    r = { "Source" => "{{src.home}}/x" }
    result = Twin::Template.substitute_record(r, {}, context: "test")
    assert_equal r, result
  end

  def test_substitute_record_raises_on_unknown_token
    r = { "Source" => "{{oops}}/path" }
    assert_raises(RuntimeError) { Twin::Template.substitute_record(r, vars, context: "test") }
  end
end

class TestConfigVarMap < Minitest::Test
  HOSTS = {
    "hosts"  => {
      "mini" => { "home" => "/Volumes/ext", "git" => "/Volumes/git" },
      "book" => { "home" => "/Users/ralf", "git" => "/Users/ralf/git", "mount" => "/Volumes/ralf" },
    },
    "host"   => "mini",
    "target" => "book",
    "sync_dir" => "/tmp",
  }.freeze

  def cfg(**overrides) = Twin::Config.new(HOSTS.merge(overrides))

  def test_builds_flat_map
    m = cfg.var_map
    assert_equal "/Volumes/ext",  m["src.home"]
    assert_equal "/Volumes/git",  m["src.git"]
    assert_equal "/Users/ralf",   m["dst.home"]
    assert_equal "/Users/ralf/git", m["dst.git"]
    assert_equal "/Volumes/ralf", m["dst.mount"]
  end

  def test_empty_when_no_hosts_configured
    assert_equal({}, Twin::Config.new("sync_dir" => "/tmp").var_map)
  end

  def test_raises_on_missing_host
    err = assert_raises(RuntimeError) { cfg("host" => "unknown").var_map }
    assert_includes err.message, "unknown"
  end

  def test_raises_on_missing_target
    err = assert_raises(RuntimeError) { cfg("target" => "unknown").var_map }
    assert_includes err.message, "unknown"
  end

  def test_raises_when_host_not_set
    err = assert_raises(RuntimeError) { cfg("host" => "").var_map }
    assert_includes err.message, "host not set"
  end
end

class TestJobTargetPath < Minitest::Test
  def job(**kw)
    defaults = {
      program: "p", path: "a/b.txt", description: "", active: 1, excludes: [],
      label: "", source: "/src", target: "/tgt", cmd: "", sync_file: "",
      source_exists: false, target_exists: false,
      source_mtime: nil, target_mtime: nil, conflict: false,
    }
    Twin::Job.new(**defaults.merge(kw))
  end

  def test_target_path_defaults_to_path
    assert_equal "/tgt/a/b.txt", job.target_path
  end

  def test_target_path_field_overrides
    j = job(target_path_field: "lib/x/b.txt")
    assert_equal "/tgt/lib/x/b.txt", j.target_path
  end

  def test_source_path_always_uses_path
    j = job(target_path_field: "other.txt")
    assert_equal "/src/a/b.txt", j.source_path
  end
end

class TestRenderJob < Minitest::Test
  HOST_CFG = {
    "hosts"  => {
      "mini" => { "home" => "/mini-home" },
      "book" => { "home" => "/book-home", "mount" => "/mnt/book" },
    },
    "host"   => "mini",
    "target" => "book",
    "sync_dir" => "/tmp",
  }.freeze

  def cfg = Twin::Config.new(HOST_CFG)

  def job(src:, tgt:, tgt_path: nil, cmd: "")
    Twin::Job.new(
      program: "p", path: File.basename(src), description: "", active: 1,
      excludes: [], label: "", source: File.dirname(src), target: File.dirname(tgt),
      cmd: cmd, delete: false, render: true,
      target_path_field: tgt_path ? File.basename(tgt_path) : nil,
      sync_file: "", source_exists: true, target_exists: false,
      source_mtime: nil, target_mtime: nil, conflict: false,
    )
  end

  def test_renders_tokens_and_writes_file
    Dir.mktmpdir do |d|
      src = File.join(d, "t.conf"); File.write(src, "home={{dst.home}}\n")
      tgt = File.join(d, "out", "t.conf")
      success, _out, changed = Twin::Sync.run_job(cfg, job(src: src, tgt: tgt))
      assert success
      assert changed
      assert_equal "home=/book-home\n", File.read(tgt)
    end
  end

  def test_skips_write_when_content_identical
    Dir.mktmpdir do |d|
      src = File.join(d, "t.conf"); File.write(src, "home=/book-home\n")
      tgt = File.join(d, "t.conf.out"); File.write(tgt, "home=/book-home\n")
      j = Twin::Job.new(
        program: "p", path: "t.conf", description: "", active: 1, excludes: [],
        label: "", source: d, target: d, cmd: "", delete: false, render: true,
        target_path_field: "t.conf.out", sync_file: "",
        source_exists: true, target_exists: true,
        source_mtime: nil, target_mtime: nil, conflict: false,
      )
      success, out, changed = Twin::Sync.run_job(cfg, j)
      assert success
      refute changed
      assert_includes out, "unchanged"
    end
  end

  def test_errors_on_directory_source
    Dir.mktmpdir do |d|
      j = Twin::Job.new(
        program: "p", path: ".", description: "", active: 1, excludes: [],
        label: "", source: d, target: d, cmd: "", delete: false, render: true,
        target_path_field: nil, sync_file: "",
        source_exists: true, target_exists: false,
        source_mtime: nil, target_mtime: nil, conflict: false,
      )
      success, out, = Twin::Sync.run_job(cfg, j)
      refute success
      assert_includes out, "directory"
    end
  end

  def test_raises_on_unknown_token_in_content
    Dir.mktmpdir do |d|
      src = File.join(d, "t.conf"); File.write(src, "x={{unknown.var}}\n")
      tgt = File.join(d, "out.conf")
      success, out, = Twin::Sync.run_job(cfg, job(src: src, tgt: tgt))
      refute success
      assert_includes out, "{{unknown.var}}"
    end
  end

  def test_dry_run_does_not_write
    Dir.mktmpdir do |d|
      src = File.join(d, "t.conf"); File.write(src, "home={{dst.home}}\n")
      tgt = File.join(d, "out.conf")
      success, out, changed = Twin::Sync.run_job(cfg, job(src: src, tgt: tgt), dry_run: true)
      assert success
      refute changed
      refute File.exist?(tgt)
      assert_includes out, "dry-run"
    end
  end

  def test_cmd_runs_only_when_changed
    Dir.mktmpdir do |d|
      src = File.join(d, "t.conf"); File.write(src, "x={{dst.home}}\n")
      tgt = File.join(d, "out.conf")
      flag = File.join(d, "cmd-ran")
      j = job(src: src, tgt: tgt, cmd: "touch #{flag}")
      Twin::Sync.run_job(cfg, j)             # first run → changed → cmd runs
      assert File.exist?(flag), "cmd should run on change"
      File.delete(flag)
      Twin::Sync.run_job(cfg, j)             # second run → unchanged → cmd skipped
      refute File.exist?(flag), "cmd should be skipped when unchanged"
    end
  end
end

class TestRenderStatus < Minitest::Test
  HOST_CFG = {
    "hosts" => { "mini" => { "home" => "/m" }, "book" => { "home" => "/b", "mount" => "/mnt" } },
    "host" => "mini", "target" => "book", "sync_dir" => "/tmp",
  }.freeze

  # Build a render Job through the real scanner path, given a template and
  # optional existing target content.
  def render_job(template:, target: nil)
    Dir.mktmpdir do |d|
      File.write(File.join(d, "t.conf"), template)
      File.write(File.join(d, "out.conf"), target) if target
      r = {
        "Program" => "P", "Path" => "t.conf", "Source" => d, "Target" => d,
        "Target-Path" => "out.conf", "Render" => true, "_note_file" => "x.md",
        "Active" => 1,
      }
      cfg = Twin::Config.new(HOST_CFG)
      yield Twin::Scanner.build_job(r, cfg.var_map)
    end
  end

  def test_missing_target_is_missing_target
    render_job(template: "x={{dst.home}}\n") { |j| assert_equal :missing_target, j.status }
  end

  def test_outdated_target_is_source_newer
    render_job(template: "x={{dst.home}}\n", target: "x=/old\n") do |j|
      assert_equal :source_newer, j.status
    end
  end

  def test_matching_target_is_in_sync
    render_job(template: "x={{dst.home}}\n", target: "x=/b\n") do |j|
      assert_equal :in_sync, j.status
    end
  end

  def test_unresolved_token_is_source_newer
    render_job(template: "x={{bad.token}}\n", target: "x=/b\n") do |j|
      assert_equal :source_newer, j.status   # needs attention, not silently in_sync
    end
  end

  def test_render_job_never_conflicts
    render_job(template: "x={{dst.home}}\n", target: "x=/b\n") do |j|
      refute j.conflict
    end
  end
end

class TestRemote < Minitest::Test
  def test_remote_with_user
    assert Twin::Remote.remote?("ralf@server:/srv/www")
  end

  def test_remote_without_user
    assert Twin::Remote.remote?("server:/srv/www")
  end

  def test_local_absolute_path_is_not_remote
    refute Twin::Remote.remote?("/Volumes/ralf/git")
  end

  def test_local_path_with_colon_after_slash_is_not_remote
    refute Twin::Remote.remote?("/tmp/a:b")
  end

  def test_empty_is_not_remote
    refute Twin::Remote.remote?("")
  end

  def test_split
    assert_equal ["ralf@server", "/srv/www"], Twin::Remote.split("ralf@server:/srv/www")
  end

  def test_split_keeps_later_colons_in_path
    assert_equal ["server", "/a:b"], Twin::Remote.split("server:/a:b")
  end

  def test_shellesc_quotes_spaces_and_quotes
    assert_equal "'/a dir/it'\\''s'", Twin::Remote.shellesc("/a dir/it's")
  end

  # Both batch scripts travel to the far side wrapped in '...' — a single
  # quote inside would end the wrapping early (and fish parses the POSIX
  # escape for it differently, see stat_paths).
  def test_batch_scripts_stay_free_of_single_quotes
    refute_includes Twin::Remote::STAT_SCRIPT, "'"
    refute_includes Twin::Remote::MD5_SCRIPT, "'"
    refute_includes Twin::Remote::PREFLIGHT_SCRIPT, "'"
  end

  def test_preflight_parsing
    out = "rsync\tok\nstat\t-\ndate\tok\nmd5\t-\nmd5sum\tok\n"
    assert_equal({ "rsync" => true, "stat" => false, "date" => true,
                   "md5" => false, "md5sum" => true },
                 Twin::Remote.parse_preflight(out))
  end
end

class TestRemoteJobs < Minitest::Test
  def valid
    {
      "Program" => "foo", "Path" => "www",
      "Source" => "/src", "Target" => "ralf@server:/srv",
      "Description" => "", "Active" => 1,
      "Exclude" => "", "Cmd" => "", "Label" => "",
      "_note_file" => "server.md",
    }
  end

  def test_remote_target_builds_without_local_stat
    job = Twin::Scanner.build_job(valid)
    refute_nil job
    assert job.remote?
    refute job.target_exists
    assert_nil job.target_mtime
    refute job.conflict
  end

  def test_remote_target_path_joins_correctly
    job = Twin::Scanner.build_job(valid)
    assert_equal "ralf@server:/srv/www", job.target_path
  end

  def test_render_plus_remote_raises
    err = assert_raises(RuntimeError) do
      Twin::Scanner.build_job(valid.merge("Render" => true))
    end
    assert_match(/Render is not supported for remote/, err.message)
  end

  def test_unreachable_status
    job = Twin::Scanner.build_job(valid)
    job.target_unreachable = true
    assert_equal :unreachable, job.status
  end

  def test_unreachable_wins_program_aggregation
    a = Twin::Scanner.build_job(valid)
    a.target_unreachable = true
    b = Twin::Scanner.build_job(valid.merge("Target" => "/tmp"))
    prog = Twin::Program.new(name: "foo", jobs: [a, b])
    assert_equal :unreachable, prog.status
  end
end

class TestBackupArgs < Minitest::Test
  def job(**kw)
    defaults = {
      program: "p", path: "www", active: 1, excludes: [],
      source: "/src", target: "/tgt", cmd: "", delete: true,
    }
    Twin::Job.new(**defaults.merge(kw))
  end

  def cfg = Twin::Config.new("sync_dir" => "/sync", "global_excludes" => [])

  def test_delete_adds_backup_args
    args = Twin::Sync.rsync_args(cfg, job)
    assert_includes args, "--delete"
    assert_includes args, "--backup"
    assert args.any? { |a| a.start_with?("--backup-dir=/tgt/.twin-backup/") }
    assert_includes args, "--exclude=.twin-backup/"
  end

  def test_no_delete_no_backup_args
    args = Twin::Sync.rsync_args(cfg, job(delete: false))
    refute_includes args, "--backup"
    refute args.any? { |a| a.start_with?("--backup-dir=") }
  end

  def test_remote_backup_dir_uses_remote_path
    args = Twin::Sync.rsync_args(cfg, job(target: "ralf@server:/srv"))
    assert args.any? { |a| a.start_with?("--backup-dir=/srv/.twin-backup/") }
  end

  def test_backup_dir_shared_within_run
    a = Twin::Sync.rsync_args(cfg, job).find { |x| x.start_with?("--backup-dir=") }
    b = Twin::Sync.rsync_args(cfg, job).find { |x| x.start_with?("--backup-dir=") }
    assert_equal a, b
  end
end

class TestJournal < Minitest::Test
  def job
    Twin::Job.new(program: "webapp", path: "www", target: "server:/srv", active: 1)
  end

  def with_state_dir
    Dir.mktmpdir do |dir|
      old = ENV["TWIN_STATE_DIR"]
      ENV["TWIN_STATE_DIR"] = dir
      yield dir
    ensure
      ENV["TWIN_STATE_DIR"] = old
    end
  end

  def test_record_and_tail
    with_state_dir do
      Twin::Journal.record(job, success: true, transferred: true)
      Twin::Journal.record(job, success: false, transferred: false, output: "rsync: boom\n")
      entries = Twin::Journal.tail(10)
      assert_equal 2, entries.size
      assert_equal "webapp", entries[0]["program"]
      assert entries[0]["ok"]
      assert entries[0]["changed"]
      refute entries[1]["ok"]
      assert_equal "rsync: boom", entries[1]["error"]
      refute entries[0].key?("error")
    end
  end

  def test_tail_limits_and_survives_garbage
    with_state_dir do
      3.times { Twin::Journal.record(job, success: true, transferred: false) }
      File.open(Twin::Journal.log_path, "a") { |f| f.puts "not json" }
      assert_equal 1, Twin::Journal.tail(2).size   # 2 lines: garbage + 1 valid
      assert_equal 3, Twin::Journal.tail(10).size
    end
  end

  def test_tail_empty_without_file
    with_state_dir do
      assert_equal [], Twin::Journal.tail(5)
    end
  end
end

class TestAddHelpers < Minitest::Test
  def write_syncfile(dir, name, source:, target: "/tgt", body: "")
    File.write(File.join(dir, name), <<~MD)
      ---
      Active: 1
      Source: #{source}
      Target: #{target}
      ---
      #{body}
    MD
    File.join(dir, name)
  end

  def test_frontmatter_parsed_and_substituted
    Dir.mktmpdir do |dir|
      f = write_syncfile(dir, "home.md", source: "\"{{src.home}}\"")
      fm = Twin::Add.frontmatter(f, { "src.home" => "/Users/x" })
      assert_equal "/Users/x", fm["Source"]
      assert_equal "/tgt", fm["Target"]
    end
  end

  def test_frontmatter_nil_without_frontmatter
    Dir.mktmpdir do |dir|
      f = File.join(dir, "plain.md")
      File.write(f, "# just markdown\n")
      assert_nil Twin::Add.frontmatter(f)
    end
  end

  def test_candidates_matches_ancestor_source
    Dir.mktmpdir do |dir|
      write_syncfile(dir, "home.md",  source: "/Users/x")
      write_syncfile(dir, "repos.md", source: "/Users/x/git")
      write_syncfile(dir, "other.md", source: "/srv")
      cands = Twin::Add.candidates(dir, "/Users/x/git/twin")
      assert_equal %w[home.md repos.md], cands.map { |f, _| File.basename(f) }
    end
  end

  def test_candidates_no_partial_component_match
    Dir.mktmpdir do |dir|
      write_syncfile(dir, "home.md", source: "/Users/x")
      assert_empty Twin::Add.candidates(dir, "/Users/xy/foo")
    end
  end

  def test_relative_path
    assert_equal "git/twin", Twin::Add.relative_path("/Users/x", "/Users/x/git/twin")
    assert_equal ".", Twin::Add.relative_path("/Users/x", "/Users/x")
  end

  def test_suggest_excludes
    Dir.mktmpdir do |dir|
      FileUtils.mkdir_p(File.join(dir, ".git"))
      FileUtils.mkdir_p(File.join(dir, "node_modules"))
      assert_equal [".git/", "node_modules/"], Twin::Add.suggest_excludes(dir)
    end
  end

  def test_suggest_excludes_empty_for_file
    Tempfile.create("f") do |f|
      assert_empty Twin::Add.suggest_excludes(f.path)
    end
  end

  def test_build_block_minimal
    block = Twin::Add.build_block(program: "fish", path: ".config/fish")
    assert_includes block, "## fish"
    assert_includes block, "Program: fish"
    assert_includes block, "Path: .config/fish"
    assert_includes block, "TODO: document why"
    refute_includes block, "Exclude:"
    refute_includes block, "Delete:"
    refute_includes block, "Cmd:"
  end

  def test_build_block_full
    block = Twin::Add.build_block(
      program: "web", path: "www", description: "site",
      excludes: [".git/"], delete: true, cmd: "ssh host 'reload'",
      prose: "Deployed straight from the build dir."
    )
    assert_includes block, "Exclude: .git/"
    assert_includes block, "Delete: true"
    assert_includes block, "Cmd: ssh host 'reload'"
    assert_includes block, "Deployed straight"
    refute_includes block, "TODO"
  end

  def test_frontmatter_text
    txt = Twin::Add.frontmatter_text(source: "/a", target: "h:/b", label: "x → y")
    assert txt.start_with?("---\nActive: 1\n")
    assert_includes txt, "Label: x → y"
    assert_includes txt, "Source: /a"
    assert_includes txt, "Target: h:/b"
  end
end

class TestOwnField < Minitest::Test
  def valid
    {
      "Program" => "fish", "Path" => ".config/fish",
      "Source" => "/src", "Target" => "/tgt",
      "Description" => "", "Active" => 1,
      "Exclude" => "", "Cmd" => "", "Label" => "",
      "_note_file" => "home.md",
    }
  end

  def test_own_parsed_like_exclude
    job = Twin::Scanner.build_job(valid.merge("Own" => "conf.d/local.fish, conf.d/atuin.env.fish"))
    assert_equal ["conf.d/local.fish", "conf.d/atuin.env.fish"], job.owned
  end

  def test_own_defaults_to_empty
    assert_equal [], Twin::Scanner.build_job(valid).owned
  end

  def test_own_stays_separate_from_excludes
    job = Twin::Scanner.build_job(valid.merge("Exclude" => "*.log", "Own" => "local.fish"))
    assert_equal ["*.log"],     job.excludes
    assert_equal ["local.fish"], job.owned
  end

  def test_all_excludes_merges_both
    job = Twin::Scanner.build_job(valid.merge("Exclude" => "*.log", "Own" => "local.fish"))
    assert_equal ["*.log", "local.fish"], job.all_excludes
  end

  def test_all_excludes_tolerates_nil_owned
    job = Twin::Job.new(program: "p", path: "a", excludes: ["x"], owned: nil)
    assert_equal ["x"], job.all_excludes
  end
end

class TestRsyncForceFlag < Minitest::Test
  def job(**kw)
    defaults = {
      program: "p", path: "www", active: 1, excludes: [], owned: [],
      source: "/src", target: "/tgt", cmd: "", delete: false,
    }
    Twin::Job.new(**defaults.merge(kw))
  end

  def cfg = Twin::Config.new("sync_dir" => "/sync", "global_excludes" => [])

  def test_update_is_the_default
    assert_includes Twin::Sync.rsync_args(cfg, job), "--update"
  end

  def test_force_drops_update
    refute_includes Twin::Sync.rsync_args(cfg, job, force: true), "--update"
  end

  def test_owned_paths_become_excludes
    args = Twin::Sync.rsync_args(cfg, job(owned: ["local.fish"]))
    assert_includes args, "--exclude=local.fish"
  end

  def test_excludes_and_owned_both_applied
    args = Twin::Sync.rsync_args(cfg, job(excludes: ["*.log"], owned: ["local.fish"]))
    assert_includes args, "--exclude=*.log"
    assert_includes args, "--exclude=local.fish"
  end
end

class TestConflictItemizeParsing < Minitest::Test
  def entries(output)
    # itemized runs rsync; parse the same lines directly instead.
    output.lines.filter_map do |line|
      m = Twin::Conflict::ENTRY_LINE.match(line)
      next unless m
      kind = Twin::Conflict.classify(m[1])
      next if kind == :attrs
      { rel: m[2], kind: kind }
    end
  end

  def test_classifies_changed_lines_and_drops_noise
    out = <<~OUT
      sending incremental file list
      .d..tp..... ./
      >f.st...... monitor.sh
      >f+++++++++ smoke.sh
      >f..t...... touched.sh
      .f...p..... icons/web.svg

      sent 199 bytes  received 31 bytes
    OUT
    assert_equal [
      { rel: "monitor.sh", kind: :content },
      { rel: "smoke.sh",   kind: :new },
      { rel: "touched.sh", kind: :time },
    ], entries(out)
  end

  def test_new_directory_counts_as_new
    assert_equal [{ rel: "core/stage/", kind: :new }],
                 entries(".d..tp..... core/\ncd+++++++++ core/stage/\n")
  end

  def test_deleting_lines_are_deletions
    assert_equal [{ rel: "old.rb", kind: :deleted }], entries("*deleting   old.rb\n")
  end

  def test_handles_paths_with_spaces
    assert_equal [{ rel: "My Folder/a b.txt", kind: :content }],
                 entries(">f.st...... My Folder/a b.txt\n")
  end
end

class TestConflictAssemble < Minitest::Test
  def assemble(forced, normal_rels, equal)
    Twin::Conflict.assemble(forced, Set.new(normal_rels), equal) { |rel| "conflict:#{rel}" }
  end

  def e(rel, kind) = { rel: rel, kind: kind }

  def test_normal_transfers_are_pending
    d = assemble([e("a", :content), e("b", :new), e("c", :deleted)], %w[a b c], {})
    assert_equal %w[a b c], d.pending
    assert_empty d.conflicts
    refute d.in_sync?
  end

  def test_held_back_content_change_is_a_conflict
    d = assemble([e("a", :content)], [], {})
    assert_equal ["conflict:a"], d.conflicts
    assert_empty d.pending
  end

  # The fish_variables case: only the timestamp moved, bytes identical —
  # noise, not drift, no matter which side is "newer".
  def test_time_only_with_equal_content_is_noise
    d = assemble([e("a", :time)], [], { "a" => true })
    assert_equal ["a"], d.time_only
    assert d.in_sync?
  end

  def test_time_only_with_different_content_transfers_or_conflicts
    flowing = assemble([e("a", :time)], %w[a], { "a" => false })
    assert_equal ["a"], flowing.pending

    held = assemble([e("a", :time)], [], { "a" => false })
    assert_equal ["conflict:a"], held.conflicts
  end

  def test_empty_forced_run_is_in_sync
    assert assemble([], [], {}).in_sync?
  end
end

class TestConflictContent < Minitest::Test
  def test_same_bytes_are_not_a_conflict
    Dir.mktmpdir do |dir|
      a = File.join(dir, "a"); File.write(a, "hello")
      b = File.join(dir, "b"); File.write(b, "hello")
      assert Twin::Conflict.same_content?(a, b)
    end
  end

  def test_different_bytes_are_a_conflict
    Dir.mktmpdir do |dir|
      a = File.join(dir, "a"); File.write(a, "hello")
      b = File.join(dir, "b"); File.write(b, "world")
      refute Twin::Conflict.same_content?(a, b)
    end
  end

  def test_same_size_different_content
    Dir.mktmpdir do |dir|
      a = File.join(dir, "a"); File.write(a, "aaaa")
      b = File.join(dir, "b"); File.write(b, "bbbb")
      refute Twin::Conflict.same_content?(a, b)
    end
  end

  def test_missing_file_is_not_same
    Dir.mktmpdir do |dir|
      a = File.join(dir, "a"); File.write(a, "x")
      refute Twin::Conflict.same_content?(a, File.join(dir, "nope"))
    end
  end

  def test_detect_skips_when_a_side_is_missing
    job = Twin::Job.new(program: "p", path: "a", source: "/s", target: "/t",
                        excludes: [], owned: [], render: false,
                        source_exists: true, target_exists: false)
    cfg = Twin::Config.new("sync_dir" => "/sync", "global_excludes" => [])
    assert_equal [], Twin::Conflict.detect(cfg, job)
  end

  def test_detect_skips_render_jobs
    job = Twin::Job.new(program: "p", path: "a", source: "/s", target: "/t",
                        excludes: [], owned: [], render: true,
                        source_exists: true, target_exists: true)
    cfg = Twin::Config.new("sync_dir" => "/sync", "global_excludes" => [])
    assert_equal [], Twin::Conflict.detect(cfg, job)
  end

  def test_text_detection
    Dir.mktmpdir do |dir|
      t = File.join(dir, "t"); File.write(t, "plain text")
      b = File.join(dir, "b"); File.binwrite(b, "bin\0ary")
      assert Twin::Conflict.text?(t)
      refute Twin::Conflict.text?(b)
    end
  end
end

class TestSummarizeOutput < Minitest::Test
  def full
    <<~OUT
      sending incremental file list
      .d..tp..... ./
      *deleting   obsolete.rb
      >f+++++++++ added.rb
      >f.s....... changed.rb
      .f...p..... untouched.rb

      sent 119 bytes  received 40 bytes  318.00 bytes/sec
      total size is 14  speedup is 0.09 (DRY RUN)
    OUT
  end

  def test_keeps_only_real_changes
    assert_equal [
      "*deleting   obsolete.rb",
      ">f+++++++++ added.rb",
      ">f.s....... changed.rb",
    ], Twin::Sync.summarize(full).lines.map(&:rstrip)
  end

  def test_drops_the_header_and_summary
    out = Twin::Sync.summarize(full)
    refute_includes out, "incremental file list"
    refute_includes out, "sent 119 bytes"
    refute_includes out, "total size is"
  end

  def test_drops_non_transfer_itemize_lines
    out = Twin::Sync.summarize(full)
    refute_includes out, "untouched.rb"   # .f...p — permissions only
    refute_includes out, ".d..tp"         # directory timestamps
  end

  def test_keeps_twins_own_lines
    out = Twin::Sync.summarize("sending incremental file list\ncmd: curl -sf http://x/reload\nhook ran\n")
    assert_includes out, "cmd: curl"
    assert_includes out, "hook ran"
  end

  def test_keeps_the_skipped_note
    out = Twin::Sync.summarize("sending incremental file list\n\nskipped: target is newer, source not synced\n")
    assert_includes out, "skipped: target is newer"
  end

  def test_no_op_summarizes_to_nothing
    out = Twin::Sync.summarize("sending incremental file list\n\nsent 63 bytes  received 12 bytes  150.00 bytes/sec\ntotal size is 4,001  speedup is 53.35\n")
    assert_equal "", out
  end

  def test_created_directory_is_noise
    refute_includes Twin::Sync.summarize("created directory /tgt/new\n>f+++++++++ a.rb\n"), "created directory"
  end
end

class TestDriftCandidates < Minitest::Test
  def job(**kw)
    defaults = {
      program: "p", path: "a", description: "", active: 1, excludes: [],
      label: "", source: "/src", target: "/tgt", cmd: "", sync_file: "",
      source_exists: true, target_exists: true,
      source_mtime: nil, target_mtime: nil, conflict: false,
    }
    Twin::Job.new(**defaults.merge(kw))
  end

  def test_selects_only_unverified_directories
    dir      = job(directory: true)
    file     = job
    verified = job(directory: true,
                   drift: Twin::Conflict::Drift.new(pending: [], time_only: [], conflicts: []))
    assert_equal [dir], Twin::Conflict.drift_candidates([dir, file, verified])
  end

  def test_skips_unanswerable_jobs
    jobs = [
      job(directory: true, active: 0),
      job(directory: true, target_unreachable: true),
      job(directory: true, source_exists: false),
      job(directory: true, target_exists: false),
    ]
    assert_empty Twin::Conflict.drift_candidates(jobs)
  end
end

class TestMergePrograms < Minitest::Test
  def job(program, path, sync_file)
    Twin::Job.new(
      program: program, path: path, description: "", active: 1, excludes: [],
      label: "", source: "/s", target: "/t", cmd: "", sync_file: sync_file,
      source_exists: true, target_exists: true,
      source_mtime: nil, target_mtime: nil, conflict: false,
    )
  end

  def prog(name, *jobs) = Twin::Program.new(name: name, jobs: jobs)

  def test_same_name_across_files_becomes_one_entry
    merged = Twin::Picker.merge_programs([
      prog("fileview", job("fileview", "build/fileview.app", "apps.md")),
      prog("other",    job("other", "x", "apps.md")),
      prog("fileview", job("fileview", ".config/fileview", "home.md")),
    ])
    assert_equal %w[fileview other], merged.map(&:name)
    fileview = merged.first
    assert_equal ["build/fileview.app", ".config/fileview"], fileview.jobs.map(&:path)
    assert_equal ["apps.md", "home.md"], fileview.jobs.map(&:sync_file)
  end

  def test_merge_is_case_insensitive_and_keeps_first_seen_name
    merged = Twin::Picker.merge_programs([
      prog("Ticker", job("Ticker", "Ticker.app", "apps.md")),
      prog("ticker", job("ticker", ".config/ticker", "home.md")),
    ])
    assert_equal ["Ticker"], merged.map(&:name)
    assert_equal 2, merged.first.jobs.size
  end

  def test_single_program_passes_through_unchanged
    p = prog("solo", job("solo", "a", "f.md"))
    assert_same p, Twin::Picker.merge_programs([p]).first
  end

  def test_jobs_stay_grouped_per_file_in_document_order
    merged = Twin::Picker.merge_programs([
      prog("dylan", job("dylan", "server.rb", "dylan.md"), job("dylan", "lib", "dylan.md")),
      prog("dylan", job("dylan", "conf", "home.md")),
    ])
    assert_equal [%w[server.rb dylan.md], %w[lib dylan.md], %w[conf home.md]],
                 merged.first.jobs.map { |j| [j.path, j.sync_file] }
  end
end

class TestMergeProgramsSort < Minitest::Test
  def prog(name)
    j = Twin::Job.new(
      program: name, path: "a", description: "", active: 1, excludes: [],
      label: "", source: "/s", target: "/t", cmd: "", sync_file: "f.md",
      source_exists: true, target_exists: true,
      source_mtime: nil, target_mtime: nil, conflict: false,
    )
    Twin::Program.new(name: name, jobs: [j])
  end

  def test_sorted_case_insensitively
    merged = Twin::Picker.merge_programs([prog("zsh"), prog("Ticker"), prog("adrem")])
    assert_equal %w[adrem Ticker zsh], merged.map(&:name)
  end
end

class TestVerifyFalse < Minitest::Test
  def job(**kw)
    defaults = {
      program: "p", path: "a", description: "", active: 1, excludes: [],
      label: "", source: "/src", target: "/tgt", cmd: "", sync_file: "",
      source_exists: true, target_exists: true,
      source_mtime: nil, target_mtime: nil, conflict: false,
    }
    Twin::Job.new(**defaults.merge(kw))
  end

  def test_verify_false_is_never_a_drift_candidate
    assert_empty Twin::Conflict.drift_candidates([job(directory: true, verify: false)])
  end

  def test_default_and_true_stay_candidates
    jobs = [job(directory: true), job(directory: true, verify: true)]
    assert_equal jobs, Twin::Conflict.drift_candidates(jobs)
  end

  def test_scanner_parses_verify_field
    r = { "Program" => "big", "Path" => "x", "Source" => "/s", "Target" => "/t",
          "Active" => 1, "Verify" => false, "_note_file" => "f.md" }
    assert_equal false, Twin::Scanner.build_job(r).verify
    assert_equal true,  Twin::Scanner.build_job(r.merge("Verify" => true)).verify
    assert_equal true,  Twin::Scanner.build_job(r.tap { |h| h.delete("Verify") }).verify
  end
end

class TestBracketConvention < Minitest::Test
  def prog(name, file: "f.md")
    j = Twin::Job.new(
      program: name, path: "a", description: "", active: 1, excludes: [],
      label: "", source: "/s", target: "/t", cmd: "", sync_file: file,
      source_exists: true, target_exists: true,
      source_mtime: nil, target_mtime: nil, conflict: false,
    )
    Twin::Program.new(name: name, jobs: [j])
  end

  def test_bracket_suffix_groups_under_base_name
    merged = Twin::Picker.merge_programs([
      prog("livesync", file: "binaries.md"),
      prog("livesync [agent]", file: "home.md"),
    ])
    assert_equal ["livesync"], merged.map(&:name)
    assert_equal ["livesync", "livesync [agent]"], merged.first.jobs.map(&:program)
  end

  def test_only_bracketed_variants_fall_back_to_base
    merged = Twin::Picker.merge_programs([
      prog("livesync [agent]"), prog("livesync [cli]"),
    ])
    assert_equal ["livesync"], merged.map(&:name)
  end

  def test_brackets_elsewhere_are_just_a_name
    merged = Twin::Picker.merge_programs([prog("a [x] b"), prog("a")])
    assert_equal ["a", "a [x] b"], merged.map(&:name).sort
  end
end
