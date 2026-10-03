package twin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestNonzeroRsyncExitStillReadsAsNothing(t *testing.T) {
	// `false` exits 1 without output: not a start failure, so no error.
	if got, err := Itemized([]string{"false"}); err != nil || got != nil {
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
