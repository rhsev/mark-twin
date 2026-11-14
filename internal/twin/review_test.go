package twin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Findings of the 2026-09-02 review: places where the port had turned a
// Ruby abort into a silent, plausible-looking answer.

// dirPair is a job whose source and target directories both exist.
func dirPair(t *testing.T) *Job {
	t.Helper()
	d := t.TempDir()
	src, tgt := filepath.Join(d, "src"), filepath.Join(d, "tgt")
	os.MkdirAll(filepath.Join(src, "x"), 0o755)
	os.MkdirAll(filepath.Join(tgt, "x"), 0o755)
	return &Job{Program: "p", Path: "x", Active: 1, Source: src, Target: tgt,
		SourceExists: true, TargetExists: true, Directory: true}
}

func TestMissingRsyncIsAnErrorNotInSync(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no rsync, no diff
	cfg := plainConfig()
	job := dirPair(t)

	if _, err := Itemized(RsyncArgs(cfg, job, true, true)); err == nil {
		t.Fatal("Itemized must report rsync not starting")
	}
	if d, err := DetectDrift(cfg, job); err == nil || d != nil {
		t.Errorf("DetectDrift must fail, not judge: %v %v", d, err)
	}
	if _, err := Detect(cfg, job); err == nil {
		t.Error("Detect must fail, not return no conflicts")
	}
	if err := FillDrift(cfg, []*Job{job}); err == nil || job.Drift != nil {
		t.Errorf("FillDrift must return the error and leave the job unverified: %v %v", err, job.Drift)
	}
	if job.Status() != StatusUnverified {
		t.Errorf("status without a verdict: %s", job.Status())
	}

	entry := &Entry{Job: job, Rel: "a", SourcePath: write(t, job.SourcePath(), "a", "x"), TargetPath: write(t, job.TargetPath(), "a", "y")}
	if got := Diff(entry); !strings.Contains(got, "diff unavailable") {
		t.Errorf("Diff without diff must say so, got %q", got)
	}
}

func TestNonzeroRsyncExitIsAnError(t *testing.T) {
	// A failed rsync compared nothing; an empty list would read as in sync.
	if got, err := Itemized([]string{"false"}); err == nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
}

func TestBuildJobRejectsListsAndMappings(t *testing.T) {
	_, err := BuildJob(withRecord(map[string]any{"Exclude": []any{".git", "node_modules"}}), nil)
	if err == nil || !strings.Contains(err.Error(), "Exclude") || !strings.Contains(err.Error(), "foo in home.md") {
		t.Errorf("list under Exclude must name field and block: %v", err)
	}
	if _, err := BuildJob(withRecord(map[string]any{"Own": map[string]any{"a": "b"}}), nil); err == nil || !strings.Contains(err.Error(), "Own") {
		t.Errorf("mapping under Own: %v", err)
	}
	if _, err := BuildJob(withRecord(map[string]any{"Path": []any{"a"}}), nil); err == nil {
		t.Error("list under Path")
	}
}

func TestBuildJobKeepsNumbersAsWritten(t *testing.T) {
	j := mustBuild(t, withRecord(map[string]any{"Path": json.Number("1.0"), "Active": json.Number("1"), "Description": json.Number("2")}))
	if j.Path != "1.0" || j.Active != 1 || j.Description != "2" {
		t.Errorf("%q %d %q", j.Path, j.Active, j.Description)
	}
	if got := str(json.Number("007")); got != "007" {
		t.Errorf("json.Number text preserved: %q", got)
	}
}

func TestBuildJobSurfacesStatErrors(t *testing.T) {
	d := t.TempDir()
	write(t, d, "fish", "i am a file, not a directory")
	_, err := BuildJob(withRecord(map[string]any{"Source": d, "Path": "fish/config.fish", "Target": t.TempDir()}), nil)
	if err == nil || !strings.Contains(err.Error(), "not a directory") || !strings.Contains(err.Error(), "foo in home.md") {
		t.Errorf("ENOTDIR must surface with the block named: %v", err)
	}
	// Plain absence stays a status, not an error.
	j := mustBuild(t, withRecord(map[string]any{"Source": d, "Path": "nope", "Target": t.TempDir()}))
	if j.SourceExists {
		t.Error("missing source must read as missing")
	}
}

func TestSelectsByFileNameOnly(t *testing.T) {
	files := []string{"/home/sync/vps.md", "/home/sync/milan_vps.md", "/home/sync/apps_macbook.md"}
	for _, c := range []struct{ name, want string }{
		{"vps", "vps.md"},
		{"vps.md", "vps.md"},
		{"milan_vps", "milan_vps.md"},
		{"macbook", ""}, // a plain name is never a substring
		{"sync", ""},    // and never the directory
		{"*vps*", "vps.md,milan_vps.md"},
		{"*_macbook", "apps_macbook.md"},
		{"*.md", "vps.md,milan_vps.md,apps_macbook.md"},
		{"", "vps.md,milan_vps.md,apps_macbook.md"},
	} {
		var out []string
		for _, f := range files {
			ok, err := selects(c.name, f)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				out = append(out, baseName(f))
			}
		}
		if got := strings.Join(out, ","); got != c.want {
			t.Errorf("name %q: got %q, want %q", c.name, got, c.want)
		}
	}
	if _, err := selects("[", "/home/sync/vps.md"); err == nil {
		t.Error("a broken pattern must be an error")
	}
}

// fakeGrubber puts a grubber on PATH that reports one block per sync-file.
func fakeGrubber(t *testing.T, syncDir string, names ...string) {
	t.Helper()
	var recs []string
	for _, n := range names {
		write(t, syncDir, n+".md", "")
		recs = append(recs, `{"Program":"`+n+`","Path":"p","Source":"`+syncDir+`","Target":"`+syncDir+`/t","Active":1,"_note_file":"`+syncDir+"/"+n+`.md"}`)
	}
	bin := t.TempDir()
	write(t, bin, "grubber", "#!/bin/sh\ncat <<'JSON'\n["+strings.Join(recs, ",")+"]\nJSON\n")
	if err := os.Chmod(bin+"/grubber", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func TestFileArgumentsAddUpAndMustMatch(t *testing.T) {
	dir := t.TempDir()
	fakeGrubber(t, dir, "vps", "milan_vps", "apps_macbook")
	write(t, dir, "empty.md", "") // a sync-file without a block
	cfg := NewConfig()
	cfg.SyncDir = dir
	load := func(files ...string) (string, error) {
		jobs, err := LoadDefinitions(cfg, files, "", false)
		var out []string
		for _, j := range jobs {
			out = append(out, j.Program)
		}
		return strings.Join(out, ","), err
	}
	for _, c := range []struct {
		files []string
		want  string
	}{
		{nil, "vps,milan_vps,apps_macbook"},
		{[]string{"vps"}, "vps"},
		{[]string{"vps", "milan_vps"}, "vps,milan_vps"},
		{[]string{"*vps*", "vps"}, "vps,milan_vps"},                           // no job twice
		{[]string{"vps", dir + "/vps.md", dir}, "vps,milan_vps,apps_macbook"}, // nor across paths
		{[]string{"empty"}, ""},                                               // there, with nothing in it
	} {
		if got, err := load(c.files...); err != nil || got != c.want {
			t.Errorf("%v: got %q %v, want %q", c.files, got, err, c.want)
		}
	}
	if _, err := LoadDefinitions(cfg, nil, "nope", true); err == nil || !strings.Contains(err.Error(), "label") {
		t.Errorf("a label nobody carries must be an error, got %v", err)
	}
	for _, files := range [][]string{{"nope"}, {"vps", "nope"}, {"*nope*"}, {"macbook"}, {"vps,milan_vps"}} {
		if _, err := load(files...); err == nil || !strings.Contains(err.Error(), "no sync-file matches") {
			t.Errorf("%v must be an error, got %v", files, err)
		}
	}
}

func TestDryRunCreatesNoTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	job := &Job{Path: "sub/deep/file", Source: dir + "/src", Target: dir + "/tgt", Active: 1}
	for _, d := range []string{job.Source + "/sub/deep", job.Target} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, job.Source+"/sub/deep", "file", "x")
	ok, out, _ := RunJob(NewConfig(), job, true, false)
	if !ok || !Transferred(out) {
		t.Errorf("dry-run into a missing directory must list the file: %s", out)
	}
	if exists(dir + "/tgt/sub") {
		t.Error("dry-run created the target directory")
	}
}

func TestJournalTailNegativeCount(t *testing.T) {
	t.Setenv("TWIN_STATE_DIR", t.TempDir())
	RecordJournal(&Job{Program: "p", Path: "a"}, true, false, "")
	if got := JournalTail(-5); len(got) != 0 {
		t.Errorf("got %d entries", len(got))
	}
}

func TestRenderOnADirectoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"/src/conf", "/tgt/conf"} {
		if err := os.MkdirAll(dir+d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := BuildJob(map[string]any{"Program": "p", "Path": "conf", "Source": dir + "/src",
		"Target": dir + "/tgt", "Active": "1", "Render": true}, nil)
	if err == nil || !strings.Contains(err.Error(), "Render needs a file") {
		t.Errorf("got %v", err)
	}
}

func TestBuildBlockWritesValuesYAMLReadsBack(t *testing.T) {
	block := BuildBlock("p", "x", "sync: all", nil, false, "echo done # restart", "")
	body := strings.Split(strings.Split(block, "```yaml\n")[1], "```")[0]
	var got map[string]string
	if err := yaml.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if got["Description"] != "sync: all" || got["Cmd"] != "echo done # restart" {
		t.Errorf("%v", got)
	}
}

func TestJournalIsTrimmedPastItsLimit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TWIN_STATE_DIR", dir)
	line := `{"ts":"2026-01-01T00:00:00+01:00","program":"p","path":"a","target":"t","ok":true,"changed":false}` + "\n"
	n := journalMaxBytes/len(line) + 10
	if err := os.WriteFile(LogPath(), []byte(strings.Repeat(line, n)), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordJournal(&Job{Program: "newest", Path: "a"}, true, false, "")
	data, _ := os.ReadFile(LogPath())
	if got := strings.Count(string(data), "\n"); got != journalKeepLines {
		t.Errorf("kept %d lines, want %d", got, journalKeepLines)
	}
	if tail := JournalTail(1); len(tail) != 1 || tail[0].Program != "newest" {
		t.Errorf("the newest entry must survive: %v", tail)
	}
	RecordJournal(&Job{Program: "p", Path: "a"}, true, false, "")
	data, _ = os.ReadFile(LogPath())
	if got := strings.Count(string(data), "\n"); got != journalKeepLines+1 {
		t.Errorf("a journal under the limit must only grow: %d lines", got)
	}
}

func TestPruneRemovesOnlyOldBackupRuns(t *testing.T) {
	target := t.TempDir()
	for _, name := range []string{"2026-01-05_120000", "2026-03-01_090000", "2026-03-20_090000", "notes"} {
		if err := os.MkdirAll(filepath.Join(BackupDir(target), name, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := ListBackups(target)
	if err != nil || strings.Join(runs, ",") != "2026-01-05_120000,2026-03-01_090000,2026-03-20_090000" {
		t.Fatalf("%v %v", runs, err)
	}
	old := OldBackups(runs, time.Date(2026, 3, 10, 0, 0, 0, 0, time.Local))
	if strings.Join(old, ",") != "2026-01-05_120000,2026-03-01_090000" {
		t.Fatalf("old: %v", old)
	}
	if err := RemoveBackups(target, old); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(BackupDir(target))
	if len(left) != 2 || left[0].Name() != "2026-03-20_090000" || left[1].Name() != "notes" {
		t.Errorf("left: %v", left)
	}
	if err := RemoveBackups(target, []string{"../elsewhere"}); err == nil {
		t.Error("a name that is no stamp must be refused")
	}
	if runs, err := ListBackups(t.TempDir()); err != nil || len(runs) != 0 {
		t.Errorf("no backup directory: %v %v", runs, err)
	}
}

func TestBackupTargetsAreThoseOfDeleteJobs(t *testing.T) {
	jobs := []*Job{{Target: "/b", Delete: true}, {Target: "/a"}, {Target: "/b", Delete: true}, {Target: "h:/c", Delete: true}}
	if got := strings.Join(BackupTargets(jobs), ","); got != "/b,h:/c" {
		t.Error(got)
	}
}

func TestNewRunStampStartsANewBackupDir(t *testing.T) {
	stampMu.Lock()
	stamp = "2026-01-01_000000"
	stampMu.Unlock()
	if runStamp() != "2026-01-01_000000" {
		t.Fatal("jobs of one run share the stamp")
	}
	NewRunStamp()
	if runStamp() == "2026-01-01_000000" {
		t.Error("a new run needs a new stamp")
	}
}
