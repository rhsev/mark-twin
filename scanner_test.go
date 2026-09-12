package twin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkJob(program, path, syncFile string) *Job {
	return testJob(func(j *Job) { j.Program, j.Path, j.SyncFile = program, path, syncFile })
}

func names(programs []*Program) []string {
	out := make([]string, len(programs))
	for i, p := range programs {
		out[i] = p.Name
	}
	return out
}

func paths(jobs []*Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.Path
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

func TestScannerGrouping(t *testing.T) {
	programs := Group([]*Job{mkJob("A", "x", "f.md"), mkJob("B", "y", "f.md"), mkJob("A", "z", "f.md")})
	if len(programs) != 2 || len(programs[0].Jobs) != 2 {
		t.Errorf("group within file: %v", names(programs))
	}

	programs = Group([]*Job{mkJob("grubber", ".config/grubber", "home.md"), mkJob("grubber", "rhsev/grubber", "repos.md")})
	if len(programs) != 2 {
		t.Errorf("same program in different files must stay separate: %d", len(programs))
	}

	// Sync-files depend on this: a Cmd that restarts a service goes in the
	// last block so it fires once every path is in place.
	dylan := Group([]*Job{
		mkJob("dylan", "server.rb", "f.md"), mkJob("dylan", "lib", "f.md"),
		mkJob("other", "elsewhere", "f.md"),
		mkJob("dylan", "plugins", "f.md"), mkJob("dylan", "config", "f.md"),
	})[0]
	want := []string{"server.rb", "lib", "plugins", "config"}
	if !eq(paths(dylan.Jobs), want) || !eq(paths(dylan.ActiveJobs()), want) {
		t.Errorf("document order: %v", paths(dylan.Jobs))
	}

	if got := names(Group([]*Job{mkJob("B", "x", "f.md"), mkJob("A", "y", "f.md"), mkJob("B", "z", "f.md")})); !eq(got, []string{"B", "A"}) {
		t.Errorf("first appearance order: %v", got)
	}
}

func validRecord() map[string]any {
	return map[string]any{
		"Program": "foo", "Path": ".config/foo",
		"Source": "/src", "Target": "/tgt",
		"Description": "desc", "Active": float64(1),
		"Exclude": "", "Cmd": "", "Label": "",
		"_note_file": "home.md",
	}
}

func withRecord(over map[string]any) map[string]any {
	r := validRecord()
	for k, v := range over {
		r[k] = v
	}
	return r
}

func mustBuild(t *testing.T, r map[string]any) *Job {
	t.Helper()
	j, err := BuildJob(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected a job")
	}
	return j
}

func TestScannerBuildJob(t *testing.T) {
	j := mustBuild(t, validRecord())
	if j.Program != "foo" || j.Path != ".config/foo" {
		t.Errorf("valid record: %+v", j)
	}
	for _, field := range []string{"Path", "Source", "Target"} {
		if j, _ := BuildJob(withRecord(map[string]any{field: ""}), nil); j != nil {
			t.Errorf("missing %s must yield nil", field)
		}
	}
	if got := mustBuild(t, withRecord(map[string]any{"Exclude": "*.tmp, .git"})).Excludes; !eq(got, []string{"*.tmp", ".git"}) {
		t.Errorf("excludes: %v", got)
	}
	if got := mustBuild(t, withRecord(map[string]any{"Active": nil})).Active; got != 0 {
		t.Errorf("active defaults to zero: %d", got)
	}
	if got := mustBuild(t, withRecord(map[string]any{"Cmd": "curl http://mi.lan/reload"})).Cmd; got != "curl http://mi.lan/reload" {
		t.Errorf("cmd: %s", got)
	}
}

// fileJobFor builds a real file pair whose target mtime is offset from the
// source's.
func fileJobFor(t *testing.T, offset time.Duration, srcContent, tgtContent string) *Job {
	t.Helper()
	dir := t.TempDir()
	src, tgt := filepath.Join(dir, "src"), filepath.Join(dir, "tgt")
	os.MkdirAll(src, 0o755)
	os.MkdirAll(tgt, 0o755)
	now := time.Now()
	os.WriteFile(filepath.Join(src, "a"), []byte(srcContent), 0o644)
	os.WriteFile(filepath.Join(tgt, "a"), []byte(tgtContent), 0o644)
	os.Chtimes(filepath.Join(src, "a"), now, now)
	os.Chtimes(filepath.Join(tgt, "a"), now.Add(offset), now.Add(offset))
	return mustBuild(t, withRecord(map[string]any{"Path": "a", "Source": src, "Target": tgt}))
}

func TestScannerConflicts(t *testing.T) {
	if fileJobFor(t, 30*time.Second, "x", "y").Conflict {
		t.Error("within tolerance must not flag")
	}
	if !fileJobFor(t, 120*time.Second, "x", "y").Conflict {
		t.Error("beyond tolerance must flag")
	}
	// The six false alarms of 2026-08-25: a newer timestamp over identical
	// bytes is a hand-copy, not a conflict.
	j := fileJobFor(t, 120*time.Second, "x", "x")
	if j.Conflict || j.ContentEqual == nil || !*j.ContentEqual || j.Status() != StatusInSync {
		t.Errorf("identical content is never a conflict: %+v", j)
	}

	dir := t.TempDir()
	src, tgt := filepath.Join(dir, "src", "d"), filepath.Join(dir, "tgt", "d")
	os.MkdirAll(src, 0o755)
	os.MkdirAll(tgt, 0o755)
	now := time.Now()
	os.Chtimes(src, now.Add(-time.Hour), now.Add(-time.Hour))
	os.Chtimes(tgt, now, now) // target dir "newer", as after every sync
	d := mustBuild(t, withRecord(map[string]any{"Path": "d", "Source": filepath.Dir(src), "Target": filepath.Dir(tgt)}))
	if !d.Directory || d.Conflict || d.Status() != StatusUnverified {
		t.Errorf("directory job never conflicts on mtime: %+v", d)
	}
}

func TestScannerResolveFileArg(t *testing.T) {
	for _, in := range []string{"", "home.md"} {
		scan, filter, err := ResolveFileArg(in)
		if err != nil || scan != "" || filter != in {
			t.Errorf("%q: %q %q %v", in, scan, filter, err)
		}
	}
	dir := t.TempDir()
	if scan, filter, _ := ResolveFileArg(dir); scan != dir || filter != "" {
		t.Errorf("absolute dir: %q %q", scan, filter)
	}
	f := filepath.Join(dir, "twin-x.md")
	os.WriteFile(f, nil, 0o644)
	if scan, filter, _ := ResolveFileArg(f); scan != dir || filter != "twin-x.md" {
		t.Errorf("absolute file: %q %q", scan, filter)
	}
	if _, _, err := ResolveFileArg("/no/such/path.md"); err == nil {
		t.Error("missing absolute path must error")
	}
}

func TestRemoteJobs(t *testing.T) {
	remote := func(over map[string]any) map[string]any {
		return withRecord(mergeMaps(map[string]any{
			"Path": "www", "Target": "ralf@server:/srv", "Description": "", "_note_file": "server.md",
		}, over))
	}
	j := mustBuild(t, remote(nil))
	if !j.IsRemote() || j.TargetExists || j.TargetMtime != nil || j.Conflict {
		t.Errorf("remote target builds without local stat: %+v", j)
	}
	if got := j.TargetPath(); got != "ralf@server:/srv/www" {
		t.Errorf("remote target path: %s", got)
	}
	if _, err := BuildJob(remote(map[string]any{"Render": true}), nil); err == nil || !strings.Contains(err.Error(), "Render is not supported for remote") {
		t.Errorf("render plus remote must raise: %v", err)
	}
	j.TargetUnreachable = true
	if j.Status() != StatusUnreachable {
		t.Error("unreachable status")
	}
	b := mustBuild(t, remote(map[string]any{"Target": "/tmp"}))
	if got := (&Program{Name: "foo", Jobs: []*Job{j, b}}).Status(); got != StatusUnreachable {
		t.Errorf("unreachable wins aggregation: %s", got)
	}
}

func mergeMaps(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

func TestOwnField(t *testing.T) {
	fish := func(over map[string]any) *Job {
		return mustBuild(t, withRecord(mergeMaps(map[string]any{"Program": "fish", "Path": ".config/fish", "Description": ""}, over)))
	}
	if got := fish(map[string]any{"Own": "conf.d/local.fish, conf.d/atuin.env.fish"}).Owned; !eq(got, []string{"conf.d/local.fish", "conf.d/atuin.env.fish"}) {
		t.Errorf("own parsed like exclude: %v", got)
	}
	if got := fish(nil).Owned; len(got) != 0 {
		t.Errorf("own defaults to empty: %v", got)
	}
	j := fish(map[string]any{"Exclude": "*.log", "Own": "local.fish"})
	if !eq(j.Excludes, []string{"*.log"}) || !eq(j.Owned, []string{"local.fish"}) {
		t.Errorf("own stays separate: %v %v", j.Excludes, j.Owned)
	}
	if got := j.AllExcludes(); !eq(got, []string{"*.log", "local.fish"}) {
		t.Errorf("all excludes merges both: %v", got)
	}
}

func TestVerifyField(t *testing.T) {
	r := map[string]any{"Program": "big", "Path": "x", "Source": "/s", "Target": "/t", "Active": float64(1), "Verify": false, "_note_file": "f.md"}
	if mustBuild(t, r).Verify() {
		t.Error("Verify: false must be parsed")
	}
	r["Verify"] = true
	if !mustBuild(t, r).Verify() {
		t.Error("Verify: true")
	}
	delete(r, "Verify")
	if !mustBuild(t, r).Verify() {
		t.Error("Verify defaults to true")
	}
}

func hostConfig() *Config {
	cfg := NewConfig()
	cfg.SyncDir = "/tmp"
	cfg.Hosts = map[string]map[string]string{
		"mini": {"home": "/m"},
		"book": {"home": "/b", "mount": "/mnt"},
	}
	cfg.Host, cfg.Target = "mini", "book"
	return cfg
}

// renderJobFor builds a render job through the real scanner path.
func renderJobFor(t *testing.T, template string, target *string) *Job {
	t.Helper()
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "t.conf"), []byte(template), 0o644)
	if target != nil {
		os.WriteFile(filepath.Join(d, "out.conf"), []byte(*target), 0o644)
	}
	vars, _ := hostConfig().VarMap()
	j, err := BuildJob(map[string]any{
		"Program": "P", "Path": "t.conf", "Source": d, "Target": d,
		"Target-Path": "out.conf", "Render": true, "_note_file": "x.md", "Active": float64(1),
	}, vars)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func sp(s string) *string { return &s }

func TestRenderStatus(t *testing.T) {
	if got := renderJobFor(t, "x={{dst.home}}\n", nil).Status(); got != StatusMissingTarget {
		t.Errorf("missing target: %s", got)
	}
	if got := renderJobFor(t, "x={{dst.home}}\n", sp("x=/old\n")).Status(); got != StatusSourceNewer {
		t.Errorf("outdated target: %s", got)
	}
	if got := renderJobFor(t, "x={{dst.home}}\n", sp("x=/b\n")).Status(); got != StatusInSync {
		t.Errorf("matching target: %s", got)
	}
	// needs attention, not silently in_sync
	if got := renderJobFor(t, "x={{bad.token}}\n", sp("x=/b\n")).Status(); got != StatusSourceNewer {
		t.Errorf("unresolved token: %s", got)
	}
	if renderJobFor(t, "x={{dst.home}}\n", sp("x=/b\n")).Conflict {
		t.Error("render jobs never conflict")
	}
}

func TestSplitList(t *testing.T) {
	if got := SplitList(" a, ,b ,"); !eq(got, []string{"a", "b"}) {
		t.Errorf("%v", got)
	}
	if got := SplitList(""); got == nil || len(got) != 0 {
		t.Errorf("empty must be an empty, non-nil list: %#v", got)
	}
}
