package twin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tp(t time.Time) *time.Time { return &t }
func bp(b bool) *bool           { return &b }

// testJob mirrors the Ruby test helper: an active file job with both sides
// present and no mtimes.
func testJob(mod func(*Job)) *Job {
	j := &Job{
		Program: "p", Path: "a", Active: 1, Excludes: []string{},
		Source: "/src", Target: "/tgt", SourceExists: true, TargetExists: true,
	}
	if mod != nil {
		mod(j)
	}
	return j
}

func drift(pending, timeOnly []string, conflicts []*Entry) *Drift {
	d := emptyDrift()
	d.Pending = append(d.Pending, pending...)
	d.TimeOnly = append(d.TimeOnly, timeOnly...)
	d.Conflicts = append(d.Conflicts, conflicts...)
	return d
}

func TestJobStatus(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		mod  func(*Job)
		want Status
	}{
		{"disabled", func(j *Job) { j.Active = 0 }, StatusDisabled},
		{"both_missing", func(j *Job) { j.SourceExists, j.TargetExists = false, false }, StatusBothMissing},
		{"missing_source", func(j *Job) { j.SourceExists = false }, StatusMissingSource},
		{"missing_target", func(j *Job) { j.TargetExists = false }, StatusMissingTarget},
		{"conflict", func(j *Job) { j.Conflict = true }, StatusTargetNewer},
		{"in_sync_exact", func(j *Job) { j.SourceMtime, j.TargetMtime = tp(now), tp(now) }, StatusInSync},
		{"in_sync_within_60s", func(j *Job) { j.SourceMtime, j.TargetMtime = tp(now), tp(now.Add(-30*time.Second)) }, StatusInSync},
		{"source_newer", func(j *Job) { j.SourceMtime, j.TargetMtime = tp(now), tp(now.Add(-time.Hour)) }, StatusSourceNewer},
		{"target_newer", func(j *Job) { j.SourceMtime, j.TargetMtime = tp(now.Add(-time.Hour)), tp(now) }, StatusTargetNewer},
		{"content_equal_overrides_mtime_drift", func(j *Job) {
			j.SourceMtime, j.TargetMtime, j.ContentEqual = tp(now.Add(-time.Hour)), tp(now), bp(true)
		}, StatusInSync},
		// Directory jobs carry no mtime verdict — without a drift result the
		// honest answer is "not checked", never a guess from directory mtimes.
		{"directory_without_drift_is_unverified", func(j *Job) {
			j.Directory, j.SourceMtime, j.TargetMtime = true, tp(now), tp(now.Add(-time.Hour))
		}, StatusUnverified},
		{"directory_drift_in_sync", func(j *Job) { j.Directory, j.Drift = true, drift(nil, nil, nil) }, StatusInSync},
		{"directory_drift_time_only_is_in_sync", func(j *Job) { j.Directory, j.Drift = true, drift(nil, []string{"a"}, nil) }, StatusInSync},
		{"directory_drift_pending_is_source_newer", func(j *Job) { j.Directory, j.Drift = true, drift([]string{"a"}, nil, nil) }, StatusSourceNewer},
		{"directory_drift_conflict_wins", func(j *Job) {
			j.Directory, j.Drift = true, drift([]string{"a"}, nil, []*Entry{{Rel: "a"}})
		}, StatusTargetNewer},
		{"directory_missing_target_reported_before_drift", func(j *Job) { j.Directory, j.TargetExists = true, false }, StatusMissingTarget},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := testJob(c.mod).Status(); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestProgramAggregation(t *testing.T) {
	now := time.Now()
	inSync := func() *Job { return testJob(func(j *Job) { j.SourceMtime, j.TargetMtime = tp(now), tp(now) }) }
	missing := testJob(func(j *Job) { j.SourceExists = false })

	if got := (&Program{Name: "X", Jobs: []*Job{inSync(), missing}}).Status(); got != StatusMissingSource {
		t.Errorf("worst first: got %s", got)
	}
	if got := (&Program{Name: "X", Jobs: []*Job{inSync(), inSync()}}).Status(); got != StatusInSync {
		t.Errorf("all in sync: got %s", got)
	}
	inactive := testJob(func(j *Job) { j.Active = 0 })
	if got := len((&Program{Name: "X", Jobs: []*Job{inSync(), inactive}}).ActiveJobs()); got != 1 {
		t.Errorf("active_jobs: got %d", got)
	}
	// unverified must rank in the aggregation — a forgotten entry would fall
	// through to in_sync, a false green.
	unverified := testJob(func(j *Job) { j.Directory = true })
	if got := (&Program{Name: "X", Jobs: []*Job{inSync(), unverified}}).Status(); got != StatusUnverified {
		t.Errorf("unverified above in_sync: got %s", got)
	}
	if got := (&Program{Name: "X", Jobs: []*Job{unverified, missing}}).Status(); got != StatusMissingSource {
		t.Errorf("unverified below problems: got %s", got)
	}
}

func TestJobTargetPath(t *testing.T) {
	j := testJob(func(j *Job) { j.Path = "a/b.txt" })
	if got := j.TargetPath(); got != "/tgt/a/b.txt" {
		t.Errorf("default target path: %s", got)
	}
	j.TargetPathField = "lib/x/b.txt"
	if got := j.TargetPath(); got != "/tgt/lib/x/b.txt" {
		t.Errorf("Target-Path override: %s", got)
	}
	if got := j.SourcePath(); got != "/src/a/b.txt" {
		t.Errorf("source path ignores Target-Path: %s", got)
	}
}

func TestAllExcludes(t *testing.T) {
	j := &Job{Excludes: []string{"x"}, Owned: nil}
	if got := j.AllExcludes(); len(got) != 1 || got[0] != "x" {
		t.Errorf("nil owned tolerated: %v", got)
	}
}

func TestMountedRootIsNot(t *testing.T) {
	if Mounted("/") {
		t.Error("root filesystem must not count as mounted")
	}
	if Mounted(filepath.Join(t.TempDir(), "nope")) {
		t.Error("missing path must not count as mounted")
	}
	_ = os.TempDir
}
