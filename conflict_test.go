package twin

import (
	"os"
	"path/filepath"
	"testing"
)

func kinds(entries []ItemizedEntry) string {
	s := ""
	for _, e := range entries {
		s += e.Rel + ":" + string(e.Kind) + ";"
	}
	return s
}

func TestConflictItemizeParsing(t *testing.T) {
	out := `sending incremental file list
.d..tp..... ./
>f.st...... monitor.sh
>f+++++++++ smoke.sh
>f..t...... touched.sh
.f...p..... icons/web.svg

sent 199 bytes  received 31 bytes
`
	if got := kinds(ParseItemized(out)); got != "monitor.sh:content;smoke.sh:new;touched.sh:time;" {
		t.Errorf("classifies and drops noise: %s", got)
	}
	if got := kinds(ParseItemized(".d..tp..... core/\ncd+++++++++ core/stage/\n")); got != "core/stage/:new;" {
		t.Errorf("new directory counts as new: %s", got)
	}
	if got := kinds(ParseItemized("*deleting   old.rb\n")); got != "old.rb:deleted;" {
		t.Errorf("deleting lines: %s", got)
	}
	if got := kinds(ParseItemized(">f.st...... My Folder/a b.txt\n")); got != "My Folder/a b.txt:content;" {
		t.Errorf("paths with spaces: %s", got)
	}
}

func set(rels ...string) map[string]bool {
	m := map[string]bool{}
	for _, r := range rels {
		m[r] = true
	}
	return m
}

func rels(entries []*Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Rel
	}
	return out
}

func assemble(forced []ItemizedEntry, normal, equal map[string]bool) *Drift {
	return Assemble(forced, normal, equal, func(rel string) *Entry { return &Entry{Rel: "conflict:" + rel} })
}

func TestConflictAssemble(t *testing.T) {
	d := assemble([]ItemizedEntry{{"a", KindContent}, {"b", KindNew}, {"c", KindDeleted}}, set("a", "b", "c"), nil)
	if !eq(d.Pending, []string{"a", "b", "c"}) || len(d.Conflicts) != 0 || d.InSync() {
		t.Errorf("normal transfers are pending: %+v", d)
	}
	d = assemble([]ItemizedEntry{{"a", KindContent}}, set(), nil)
	if !eq(rels(d.Conflicts), []string{"conflict:a"}) || len(d.Pending) != 0 {
		t.Errorf("held back content change is a conflict: %+v", d)
	}
	// The fish_variables case: only the timestamp moved, bytes identical —
	// noise, not drift, no matter which side is "newer".
	d = assemble([]ItemizedEntry{{"a", KindTime}}, set(), map[string]bool{"a": true})
	if !eq(d.TimeOnly, []string{"a"}) || !d.InSync() {
		t.Errorf("time only with equal content is noise: %+v", d)
	}
	flowing := assemble([]ItemizedEntry{{"a", KindTime}}, set("a"), map[string]bool{"a": false})
	held := assemble([]ItemizedEntry{{"a", KindTime}}, set(), map[string]bool{"a": false})
	if !eq(flowing.Pending, []string{"a"}) || !eq(rels(held.Conflicts), []string{"conflict:a"}) {
		t.Errorf("time only with different content: %+v %+v", flowing, held)
	}
	if !assemble(nil, set(), nil).InSync() {
		t.Error("empty forced run is in sync")
	}
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConflictContent(t *testing.T) {
	dir := t.TempDir()
	if !SameContent(write(t, dir, "a", "hello"), write(t, dir, "b", "hello")) {
		t.Error("same bytes")
	}
	if SameContent(write(t, dir, "c", "hello"), write(t, dir, "d", "world")) {
		t.Error("different bytes")
	}
	if SameContent(write(t, dir, "e", "aaaa"), write(t, dir, "f", "bbbb")) {
		t.Error("same size, different content")
	}
	if SameContent(write(t, dir, "g", "x"), filepath.Join(dir, "nope")) {
		t.Error("missing file is not same")
	}

	cfg := plainConfig()
	missing := &Job{Program: "p", Path: "a", Source: "/s", Target: "/t", SourceExists: true, TargetExists: false}
	if got, err := Detect(cfg, missing); err != nil || len(got) != 0 {
		t.Error("detect skips when a side is missing")
	}
	render := &Job{Program: "p", Path: "a", Source: "/s", Target: "/t", Render: true, SourceExists: true, TargetExists: true}
	if got, err := Detect(cfg, render); err != nil || len(got) != 0 {
		t.Error("detect skips render jobs")
	}

	if !IsText(write(t, dir, "t", "plain text")) {
		t.Error("text detection: text")
	}
	if IsText(write(t, dir, "bin", "bin\x00ary")) {
		t.Error("text detection: binary")
	}
}

func TestDriftCandidates(t *testing.T) {
	dir := testJob(func(j *Job) { j.Directory = true })
	file := testJob(nil)
	verified := testJob(func(j *Job) { j.Directory, j.Drift = true, emptyDrift() })
	got := DriftCandidates([]*Job{dir, file, verified})
	if len(got) != 1 || got[0] != dir {
		t.Errorf("selects only unverified directories: %v", got)
	}
	unanswerable := []*Job{
		testJob(func(j *Job) { j.Directory, j.Active = true, 0 }),
		testJob(func(j *Job) { j.Directory, j.TargetUnreachable = true, true }),
		testJob(func(j *Job) { j.Directory, j.SourceExists = true, false }),
		testJob(func(j *Job) { j.Directory, j.TargetExists = true, false }),
	}
	if got := DriftCandidates(unanswerable); len(got) != 0 {
		t.Errorf("skips unanswerable jobs: %v", got)
	}
	if got := DriftCandidates([]*Job{testJob(func(j *Job) { j.Directory, j.SkipVerify = true, true })}); len(got) != 0 {
		t.Error("Verify: false is never a drift candidate")
	}
	both := []*Job{testJob(func(j *Job) { j.Directory = true }), testJob(func(j *Job) { j.Directory = true })}
	if got := DriftCandidates(both); len(got) != 2 {
		t.Error("default and true stay candidates")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]ChangeKind{
		"*deleting": KindDeleted, ">f+++++++++": KindNew, "cd+++++++++": KindNew,
		">f.s.......": KindContent, ">f..t......": KindTime, ">f..T......": KindTime,
		".f...p.....": KindAttrs, ">f": KindAttrs,
	}
	for flags, want := range cases {
		if got := Classify(flags); got != want {
			t.Errorf("%s: %s, want %s", flags, got, want)
		}
	}
}
