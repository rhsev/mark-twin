package twin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatDelta(t *testing.T) {
	now := time.Now()
	cases := []struct {
		sm, tm *time.Time
		want   string
	}{
		{tp(now), tp(now), "in sync"},
		{tp(now), tp(now.Add(-300 * time.Second)), "src +5m"},
		{tp(now), tp(now.Add(-3 * time.Hour)), "src +3h"},
		{tp(now.Add(-2 * 24 * time.Hour)), tp(now), "tgt +2d"},
		{nil, nil, ""},
	}
	for _, c := range cases {
		if got := FormatDelta(c.sm, c.tm); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func prog(name string, jobs ...*Job) *Program {
	if len(jobs) == 0 {
		jobs = []*Job{mkJob(name, "a", "f.md")}
	}
	return &Program{Name: name, Jobs: jobs}
}

func TestMergePrograms(t *testing.T) {
	merged := MergePrograms([]*Program{
		prog("fileview", mkJob("fileview", "build/fileview.app", "apps.md")),
		prog("other", mkJob("other", "x", "apps.md")),
		prog("fileview", mkJob("fileview", ".config/fileview", "home.md")),
	})
	if !eq(names(merged), []string{"fileview", "other"}) {
		t.Errorf("same name across files becomes one entry: %v", names(merged))
	}
	fv := merged[0]
	if !eq(paths(fv.Jobs), []string{"build/fileview.app", ".config/fileview"}) || fv.Jobs[0].SyncFile != "apps.md" || fv.Jobs[1].SyncFile != "home.md" {
		t.Errorf("merged jobs: %v", paths(fv.Jobs))
	}

	merged = MergePrograms([]*Program{
		prog("Ticker", mkJob("Ticker", "Ticker.app", "apps.md")),
		prog("ticker", mkJob("ticker", ".config/ticker", "home.md")),
	})
	if !eq(names(merged), []string{"Ticker"}) || len(merged[0].Jobs) != 2 {
		t.Errorf("case insensitive, keeps first seen name: %v", names(merged))
	}

	solo := prog("solo", mkJob("solo", "a", "f.md"))
	if MergePrograms([]*Program{solo})[0] != solo {
		t.Error("single program passes through unchanged")
	}

	merged = MergePrograms([]*Program{
		prog("dylan", mkJob("dylan", "server.rb", "dylan.md"), mkJob("dylan", "lib", "dylan.md")),
		prog("dylan", mkJob("dylan", "conf", "home.md")),
	})
	if !eq(paths(merged[0].Jobs), []string{"server.rb", "lib", "conf"}) {
		t.Errorf("jobs stay grouped per file in document order: %v", paths(merged[0].Jobs))
	}

	if got := names(MergePrograms([]*Program{prog("zsh"), prog("Ticker"), prog("adrem")})); !eq(got, []string{"adrem", "Ticker", "zsh"}) {
		t.Errorf("sorted case-insensitively: %v", got)
	}
}

func TestBracketConvention(t *testing.T) {
	merged := MergePrograms([]*Program{
		prog("livesync", mkJob("livesync", "a", "binaries.md")),
		prog("livesync [agent]", mkJob("livesync [agent]", "a", "home.md")),
	})
	if !eq(names(merged), []string{"livesync"}) || merged[0].Jobs[0].Program != "livesync" || merged[0].Jobs[1].Program != "livesync [agent]" {
		t.Errorf("bracket suffix groups under base name: %v", names(merged))
	}
	if got := names(MergePrograms([]*Program{prog("livesync [agent]"), prog("livesync [cli]")})); !eq(got, []string{"livesync"}) {
		t.Errorf("only bracketed variants fall back to base: %v", got)
	}
	if got := names(MergePrograms([]*Program{prog("a [x] b"), prog("a")})); !eq(got, []string{"a", "a [x] b"}) {
		t.Errorf("brackets elsewhere are just a name: %v", got)
	}
}

func TestPreview(t *testing.T) {
	fm, body := SplitFrontmatter("---\nActive: 1\n---\nbody\n")
	if !strings.Contains(fm, "Active: 1") || body != "body\n" {
		t.Errorf("split frontmatter: %q %q", fm, body)
	}
	if fm, body := SplitFrontmatter("no frontmatter"); fm != "" || body != "no frontmatter" {
		t.Errorf("no frontmatter: %q %q", fm, body)
	}
	if got := ExtractIntro([]string{"intro", "more", "## Section", "body"}); got != "intro\nmore" {
		t.Errorf("intro stops at h2: %q", got)
	}
	if got := ExtractIntro([]string{"only"}); got != "only" {
		t.Errorf("intro without h2: %q", got)
	}
	if s, e, ok := FindBlock([]string{"```yaml", "Program: foo", "Path: .config/foo", "```"}, ".config/foo"); !ok || s != 0 || e != 3 {
		t.Errorf("find block: %d %d %v", s, e, ok)
	}
	if _, _, ok := FindBlock([]string{"```yaml", "Path: other", "```"}, ".config/foo"); ok {
		t.Error("find block no match")
	}
	if _, _, ok := FindBlock([]string{"```yaml", "Path: '.config/foo'", "```"}, ".config/foo"); !ok {
		t.Error("find block quoted path")
	}

	md := "---\nActive: 1\n---\nIntro text.\n\n## Section A\n\n```yaml\nProgram: foo\nPath: .config/foo\n```\n"
	p := filepath.Join(t.TempDir(), "s.md")
	os.WriteFile(p, []byte(md), 0o644)
	out := ExtractCompact(p, ".config/foo")
	for _, want := range []string{"Active: 1", "Section A", "Path: .config/foo", "Intro text."} {
		if !strings.Contains(out, want) {
			t.Errorf("compact must include %q:\n%s", want, out)
		}
	}
	if ExtractCompact("/no/such/file.md", "foo") != "" {
		t.Error("missing file returns empty")
	}
	os.WriteFile(p, []byte("---\nActive: 1\n---\n## A\n\n```yaml\nPath: other\n```\n"), 0o644)
	out = ExtractCompact(p, "nonexistent")
	if !strings.Contains(out, "Active: 1") || strings.Contains(out, "other") {
		t.Errorf("no matching block:\n%s", out)
	}
}

func TestJournal(t *testing.T) {
	t.Setenv("TWIN_STATE_DIR", t.TempDir())
	job := &Job{Program: "webapp", Path: "www", Target: "server:/srv", Active: 1}

	RecordJournal(job, true, true, "")
	RecordJournal(job, false, false, "rsync: boom\n")
	entries := JournalTail(10)
	if len(entries) != 2 || entries[0].Program != "webapp" || !entries[0].OK || !entries[0].Changed || entries[0].Error != nil {
		t.Errorf("record and tail: %+v", entries)
	}
	if entries[1].OK || entries[1].Error == nil || *entries[1].Error != "rsync: boom" {
		t.Errorf("error entry: %+v", entries[1])
	}

	t.Setenv("TWIN_STATE_DIR", t.TempDir())
	for range 3 {
		RecordJournal(job, true, false, "")
	}
	f, _ := os.OpenFile(LogPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("not json\n")
	f.Close()
	if got := len(JournalTail(2)); got != 1 { // 2 lines: garbage + 1 valid
		t.Errorf("tail limits and survives garbage: %d", got)
	}
	if got := len(JournalTail(10)); got != 3 {
		t.Errorf("tail all: %d", got)
	}

	t.Setenv("TWIN_STATE_DIR", t.TempDir())
	if got := JournalTail(5); len(got) != 0 {
		t.Error("tail empty without file")
	}
}

func writeSyncfile(t *testing.T, dir, name, source, target string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte("---\nActive: 1\nSource: "+source+"\nTarget: "+target+"\n---\n"), 0o644)
	return p
}

func TestAddHelpers(t *testing.T) {
	dir := t.TempDir()
	f := writeSyncfile(t, dir, "home.md", `"{{src.home}}"`, "/tgt")
	fm, err := Frontmatter(f, map[string]string{"src.home": "/Users/x"})
	if err != nil || fm["Source"] != "/Users/x" || fm["Target"] != "/tgt" {
		t.Errorf("frontmatter parsed and substituted: %v %v", fm, err)
	}
	plain := filepath.Join(dir, "plain.md")
	os.WriteFile(plain, []byte("# just markdown\n"), 0o644)
	if fm, _ := Frontmatter(plain, nil); fm != nil {
		t.Error("nil without frontmatter")
	}

	cdir := t.TempDir()
	writeSyncfile(t, cdir, "home.md", "/Users/x", "/tgt")
	writeSyncfile(t, cdir, "repos.md", "/Users/x/git", "/tgt")
	writeSyncfile(t, cdir, "other.md", "/srv", "/tgt")
	cands, _ := Candidates(cdir, "/Users/x/git/twin", nil)
	var got []string
	for _, c := range cands {
		got = append(got, filepath.Base(c.File))
	}
	if !eq(got, []string{"home.md", "repos.md"}) {
		t.Errorf("candidates match ancestor source: %v", got)
	}
	if cands, _ := Candidates(cdir, "/Users/xy/foo", nil); len(cands) != 0 {
		t.Error("no partial component match")
	}

	if RelativePath("/Users/x", "/Users/x/git/twin") != "git/twin" || RelativePath("/Users/x", "/Users/x") != "." {
		t.Error("relative path")
	}

	edir := t.TempDir()
	os.MkdirAll(filepath.Join(edir, ".git"), 0o755)
	os.MkdirAll(filepath.Join(edir, "node_modules"), 0o755)
	if got := SuggestExcludes(edir); !eq(got, []string{".git/", "node_modules/"}) {
		t.Errorf("suggest excludes: %v", got)
	}
	if got := SuggestExcludes(write(t, edir, "f", "")); len(got) != 0 {
		t.Error("suggest excludes empty for file")
	}

	block := BuildBlock("fish", ".config/fish", "", nil, false, "", "")
	for _, want := range []string{"## fish", "Program: fish", "Path: .config/fish", "TODO: document why"} {
		if !strings.Contains(block, want) {
			t.Errorf("minimal block lacks %q", want)
		}
	}
	for _, absent := range []string{"Exclude:", "Delete:", "Cmd:"} {
		if strings.Contains(block, absent) {
			t.Errorf("minimal block has %q", absent)
		}
	}
	block = BuildBlock("web", "www", "site", []string{".git/"}, true, "ssh host 'reload'", "Deployed straight from the build dir.")
	for _, want := range []string{"Exclude: .git/", "Delete: true", "Cmd: ssh host 'reload'", "Deployed straight"} {
		if !strings.Contains(block, want) {
			t.Errorf("full block lacks %q", want)
		}
	}
	if strings.Contains(block, "TODO") {
		t.Error("full block has TODO")
	}

	txt := FrontmatterText("/a", "h:/b", "x → y")
	if !strings.HasPrefix(txt, "---\nActive: 1\n") {
		t.Error("frontmatter text start")
	}
	for _, want := range []string{"Label: x → y", "Source: /a", "Target: h:/b"} {
		if !strings.Contains(txt, want) {
			t.Errorf("frontmatter text lacks %q", want)
		}
	}
}

func TestJobJSONShape(t *testing.T) {
	j := testJob(func(j *Job) { j.Drift = drift([]string{"x"}, nil, []*Entry{{Rel: "y"}}) })
	out := j.ToJSON()
	if out.TargetPathField != nil || out.Drift == nil || !eq(out.Drift.Conflicts, []string{"y"}) || !out.Verify || out.Status != StatusInSync {
		t.Errorf("%+v", out)
	}
	data, err := MarshalJSON(out, "  ")
	if err != nil || !strings.Contains(string(data), `"target_path_field": null`) || strings.Contains(string(data), `\u003c`) {
		t.Errorf("json: %v\n%s", err, data)
	}
}
