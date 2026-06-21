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

  def conflict_for(target_offset)
    Dir.mktmpdir do |dir|
      src = File.join(dir, "src")
      tgt = File.join(dir, "tgt")
      FileUtils.mkdir_p(src)
      FileUtils.mkdir_p(tgt)
      now = Time.now
      File.write(File.join(src, "a"), "x")
      File.write(File.join(tgt, "a"), "x")
      File.utime(now, now, File.join(src, "a"))
      File.utime(now + target_offset, now + target_offset, File.join(tgt, "a"))
      job = Twin::Scanner.build_job(valid.merge("Path" => "a", "Source" => src, "Target" => tgt))
      return job.conflict
    end
  end

  def test_conflict_within_tolerance_not_flagged
    refute conflict_for(30)
  end

  def test_conflict_beyond_tolerance_flagged
    assert conflict_for(120)
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
