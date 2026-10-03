package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rhsev/mark-twin/internal/twin"
)

// The TUI is driven headlessly: messages go into Update, commands are
// executed inline and their results fed back, and View is inspected as
// text. No terminal, no subprocess — the renderer is a stub and the test
// jobs are files, so no rsync verification is triggered.

func stubRenderer(job *twin.Job, width int) string { return "PREVIEW " + job.Path }

func testPrograms() []*twin.Program {
	job := func(program, path, file string) *twin.Job {
		return &twin.Job{Program: program, Path: path, SyncFile: "/sync/" + file, Active: 1,
			Source: "/s", Target: "/t", SourceExists: true, TargetExists: true}
	}
	return twin.MergePrograms([]*twin.Program{
		{Name: "fish", Jobs: []*twin.Job{job("fish", ".config/fish", "home.md")}},
		{Name: "dylan", Jobs: []*twin.Job{job("dylan", "server.rb", "repos.md"), job("dylan", "lib", "repos.md")}},
		{Name: "dylan", Jobs: []*twin.Job{job("dylan", "conf", "home.md")}},
	})
}

// pump applies msg and runs every resulting command inline, feeding the
// messages back until nothing is left. Quit is reported, not executed.
func pump(m tuiModel, msg tea.Msg) (tuiModel, bool) {
	quit := false
	var step func(msg tea.Msg)
	step = func(msg tea.Msg) {
		var cmd tea.Cmd
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(tuiModel)
		drain(cmd, &quit, step)
	}
	step(msg)
	return m, quit
}

func drain(cmd tea.Cmd, quit *bool, step func(tea.Msg)) {
	if cmd == nil {
		return
	}
	switch out := cmd().(type) {
	case nil:
	case tea.QuitMsg:
		*quit = true
	case tea.BatchMsg:
		for _, c := range out {
			drain(c, quit, step)
		}
	default:
		step(out)
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func loaded(t *testing.T) tuiModel {
	t.Helper()
	m := newTUI(twin.NewConfig(), "", stubRenderer)
	m.load = func(*twin.Config, string, int) tea.Cmd { return nil } // the tests feed programsMsg themselves
	m, _ = pump(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.loadGen = 1
	m, _ = pump(m, programsMsg{gen: 1, programs: testPrograms()})
	return m
}

func TestTUIStageOne(t *testing.T) {
	m := loaded(t)
	view := m.View()
	for _, want := range []string{"dylan", "fish", "2 programs", "repos.md"} {
		if !strings.Contains(view, want) {
			t.Errorf("stage 1 view lacks %q", want)
		}
	}
	if !strings.Contains(view, "3/3 active") && !strings.Contains(view, "server.rb") {
		t.Errorf("stage 1 preview should show the program overview:\n%s", view)
	}
}

func TestTUIEnterToggleSyncSelection(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter")) // cursor on "dylan" (sorted first)
	if m.stage != stageJobs || m.program == nil || m.program.Name != "dylan" {
		t.Fatalf("enter must open stage 2 for dylan, got stage %d", m.stage)
	}
	view := m.View()
	for _, want := range []string{"server.rb", "lib", "conf", "repos.md", "home.md", "PREVIEW server.rb", "0 of 3 selected"} {
		if !strings.Contains(view, want) {
			t.Errorf("stage 2 view lacks %q:\n%s", want, view)
		}
	}

	m, _ = pump(m, key(" "))
	if len(m.selectedJobs()) != 1 || m.selectedJobs()[0].Path != "server.rb" || !strings.Contains(m.View(), "■") {
		t.Errorf("space must toggle the current row")
	}
	m, _ = pump(m, key("a"))
	if len(m.selectedJobs()) != 3 {
		t.Errorf("a must select all shown jobs, got %d", len(m.selectedJobs()))
	}
	m, _ = pump(m, key("u"))
	if got := m.selectedJobs(); len(got) != 1 {
		t.Errorf("with nothing toggled the current row is the selection, got %d", len(got))
	}
}

func TestTUIFilterAndBack(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("/"))
	if !m.filterFocused {
		t.Fatal("/ must focus the filter")
	}
	m, _ = pump(m, key("fi"))
	if len(m.shownPrograms) != 1 || m.shownPrograms[0].Name != "fish" {
		t.Errorf("filter must narrow programs, got %d", len(m.shownPrograms))
	}
	m, _ = pump(m, key("esc"))
	if m.filterFocused || len(m.shownPrograms) != 2 {
		t.Errorf("esc must clear the filter and return to the table")
	}

	m, _ = pump(m, key("enter"))
	m, _ = pump(m, key("esc"))
	if m.stage != stagePrograms || m.program != nil {
		t.Error("esc in stage 2 must return to stage 1")
	}
	_, quit := pump(m, key("esc"))
	if !quit {
		t.Error("esc in stage 1 must quit, like the fzf picker")
	}
}

func TestTUIReloadReentersProgram(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter"))
	m, _ = pump(m, syncDoneMsg{quit: false})
	if m.reenter != "dylan" || !m.loading {
		t.Fatalf("after a sync the same program must be reopened after reload: %q", m.reenter)
	}
	m, _ = pump(m, programsMsg{gen: m.loadGen, programs: testPrograms()})
	if m.stage != stageJobs || m.program == nil || m.program.Name != "dylan" || m.loading {
		t.Error("reload must land back in stage 2 of the same program")
	}
	if _, quit := pump(m, syncDoneMsg{quit: true}); !quit {
		t.Error("q at the continue prompt must quit")
	}
}

// The real start path: Init's command must deliver programs the model
// accepts. Init runs on a value copy, so a generation bumped there would
// never match — the bug that left the first build's table empty.
func TestTUIInitLoadIsAccepted(t *testing.T) {
	m := newTUI(twin.NewConfig(), "", stubRenderer)
	m.load = func(_ *twin.Config, _ string, gen int) tea.Cmd {
		return func() tea.Msg { return programsMsg{gen: gen, programs: testPrograms()} }
	}
	m, _ = pump(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	msg := m.Init()()
	m, _ = pump(m, msg)
	if len(m.programs) != 2 || m.loading {
		t.Fatalf("Init's load result must be applied: %d programs, loading=%v", len(m.programs), m.loading)
	}
	if !strings.Contains(m.View(), "dylan") {
		t.Error("table must show the loaded programs")
	}
}

// d runs the dry run inside the TUI and shows its verdict in the preview
// pane; the test jobs have no source, so no rsync is spawned.
func TestTUIDryRunShowsReportInPreview(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter"))
	m, _ = pump(m, key("d"))
	view := m.View()
	if !strings.Contains(view, "dry-run") || !strings.Contains(view, "server.rb") {
		t.Errorf("dry-run report must appear in the preview pane:\n%s", view)
	}
	if !strings.Contains(m.status, "dry-run") {
		t.Errorf("status must carry the dry-run verdict, got %q", m.status)
	}
	if got := m.changesCell(m.program.Jobs[0]); got != "skipped" {
		t.Errorf("changes column must show the verdict (target unmounted → skipped), got %q", got)
	}
	// The verdict survives moving away and back.
	m, _ = pump(m, key("j"))
	m, _ = pump(m, key("k"))
	if view := m.View(); !strings.Contains(view, "dry-run: skipped") {
		t.Errorf("dry-run verdict must stay attached to the job:\n%s", view)
	}
}

func TestColorizeRows(t *testing.T) {
	view := " st  path\n─────────\n ✓   fish/config.fish\n → lib\n\x1b[7m ✓   cursor row\x1b[0m\n"
	out := colorizeRows(view)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[2], "\x1b[32m") {
		t.Errorf("in-sync row must be green: %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], "\x1b[33m") {
		t.Errorf("source-newer row must be yellow: %q", lines[3])
	}
	if lines[0] != " st  path" || lines[1] != "─────────" {
		t.Error("header and border must stay untouched")
	}
	if lines[4] != "\x1b[7m ✓   cursor row\x1b[0m" {
		t.Errorf("a row that already carries escape codes (the cursor row) must stay untouched: %q", lines[4])
	}
}

func TestTUIStaleMessagesAreDropped(t *testing.T) {
	m := loaded(t)
	before := len(m.programs)
	m, _ = pump(m, programsMsg{gen: 99, programs: nil})
	if len(m.programs) != before {
		t.Error("a programs message from an old generation must be ignored")
	}
	m, _ = pump(m, programsMsg{gen: m.loadGen, err: errExit})
	if !m.statusErr {
		t.Error("a load error must show in the status bar")
	}
}

// A sync's outcome is kept on the row and in the preview after the reload
// that follows it, and a dry-run verdict survives a plain reload — both are
// keyed by the job's identity, not by the object a reload replaces.
func TestTUIEvidenceSurvivesReload(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter"))
	m, _ = pump(m, key("d"))
	m, _ = pump(m, key("r")) // reload: fresh job objects
	m, _ = pump(m, programsMsg{gen: m.loadGen, programs: testPrograms()})
	if got := m.changesCell(m.program.Jobs[0]); got != "skipped" {
		t.Errorf("dry-run verdict must survive a reload, got %q", got)
	}

	job := m.program.Jobs[0]
	m, _ = pump(m, syncDoneMsg{outcomes: []jobOutcome{{job: job, ok: true, transferred: true, output: ">f+++++++++ a\n>f.s....... b\n"}}})
	m, _ = pump(m, programsMsg{gen: m.loadGen, programs: testPrograms()})
	if got := m.changesCell(m.program.Jobs[0]); got != "synced 2" {
		t.Errorf("sync outcome must show on the row after reload, got %q", got)
	}
	view := m.View()
	if !strings.Contains(view, "synced: 2 changes") || !strings.Contains(view, "synced 1 of 1 changed") {
		t.Errorf("sync outcome must show in preview and status:\n%s", view)
	}
}

// Verification verdicts (the ✓ that replaces ∘ on directory rows) must
// survive the reload after a sync — except for the jobs that were synced,
// which are verified anew.
func TestTUIVerificationSurvivesReload(t *testing.T) {
	dirPrograms := func() []*twin.Program {
		dir := func(name, path string) *twin.Job {
			return &twin.Job{Program: name, Path: path, SyncFile: "/sync/a.md", Active: 1, Directory: true,
				Source: "/nonexistent", Target: "/nonexistent", SourceExists: true, TargetExists: true}
		}
		return []*twin.Program{
			{Name: "alpha", Jobs: []*twin.Job{dir("alpha", "one")}},
			{Name: "beta", Jobs: []*twin.Job{dir("beta", "two")}},
		}
	}
	m := newTUI(twin.NewConfig(), "", stubRenderer)
	m.load = func(*twin.Config, string, int) tea.Cmd { return nil }
	m, _ = pump(m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m, _ = pump(m, programsMsg{gen: m.loadGen, programs: dirPrograms()})
	if m.programs[0].Status() != twin.StatusUnverified {
		t.Fatalf("directory rows start unverified, got %s", m.programs[0].Status())
	}
	// As if `v` had verified both programs.
	for _, p := range m.programs {
		m, _ = pump(m, driftMsg{gen: m.verifyGen, job: p.Jobs[0], drift: &twin.Drift{}})
	}
	if m.programs[0].Status() != twin.StatusInSync || m.programs[1].Status() != twin.StatusInSync {
		t.Fatal("verification must turn the rows green")
	}
	// A sync of beta's job, then the reload with fresh objects.
	m, _ = pump(m, syncDoneMsg{outcomes: []jobOutcome{{job: m.programs[1].Jobs[0], ok: true}}})
	m, _ = pump(m, programsMsg{gen: m.loadGen, programs: dirPrograms()})
	if got := m.programs[0].Status(); got != twin.StatusInSync {
		t.Errorf("alpha's verdict must survive the reload, got %s", got)
	}
	if got := m.programs[1].Status(); got != twin.StatusUnverified {
		t.Errorf("beta was synced and must be verified anew, got %s", got)
	}
}
