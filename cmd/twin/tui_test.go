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

// stubSync replaces the sync's steps: the plan finds every job available
// with the given conflicts, and each job reports outcome(job). calls
// records which jobs ran and with what force.
type syncCall struct {
	path  string
	force bool
}

func stubSync(m *tuiModel, conflicts []*twin.Entry, outcome func(*twin.Job) jobOutcome) *[]syncCall {
	calls := &[]syncCall{}
	m.planSync = func(_ *twin.Config, jobs []*twin.Job) syncPlanMsg {
		return syncPlanMsg{ready: jobs, conflicts: conflicts}
	}
	m.runSyncJob = func(_ *twin.Config, job *twin.Job, force bool) jobOutcome {
		*calls = append(*calls, syncCall{job.Path, force})
		if outcome != nil {
			return outcome(job)
		}
		return jobOutcome{job: job, ok: true}
	}
	return calls
}

// syncWith runs a complete sync of program's given outcomes through the
// real message flow, as if the user had pressed enter on those jobs.
func syncWith(m tuiModel, program *twin.Program, outcomes ...jobOutcome) tuiModel {
	byJob := map[*twin.Job]jobOutcome{}
	var jobs []*twin.Job
	for _, o := range outcomes {
		byJob[o.job] = o
		jobs = append(jobs, o.job)
	}
	stubSync(&m, nil, func(j *twin.Job) jobOutcome { return byJob[j] })
	m.syncGen++
	m.run = &syncRun{program: program}
	m, _ = pump(m, syncPlanMsg{gen: m.syncGen, ready: jobs})
	return m
}

// Enter in stage 2 syncs inside the TUI: every selected job runs in order,
// its row shows the outcome at once, and the program reloads at the end
// with the cursor's program reopened.
func TestTUISyncRunsInsideTUI(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter"))
	calls := stubSync(&m, nil, func(j *twin.Job) jobOutcome {
		return jobOutcome{job: j, ok: true, transferred: j.Path == "lib", output: ">f+++++++++ a\n"}
	})
	m, _ = pump(m, key("a"))
	m, _ = pump(m, key("enter"))
	if len(*calls) != 3 || (*calls)[0].path != "server.rb" || (*calls)[0].force {
		t.Fatalf("all three jobs must run in document order without force: %+v", *calls)
	}
	if m.run != nil || m.reenter != "dylan" || !m.loading {
		t.Fatalf("a finished sync must reload and reopen dylan: run=%v reenter=%q", m.run, m.reenter)
	}
	m, _ = pump(m, programsMsg{gen: m.loadGen, programs: testPrograms()})
	if m.stage != stageJobs || m.program.Name != "dylan" {
		t.Fatal("reload must land back in stage 2 of the same program")
	}
	if !strings.Contains(m.status, "synced 1 of 3 changed") {
		t.Errorf("status must carry the verdict, got %q", m.status)
	}
	if got := m.changesCell(m.program.Jobs[1]); got != "synced 1" {
		t.Errorf("row must show the outcome, got %q", got)
	}
}

// Step by step: the status line follows the sync, and while it runs the
// keys that would start another sync or reload are ignored.
func TestTUISyncProgressAndLockedKeys(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter"))
	stubSync(&m, nil, nil)
	m, _ = pump(m, key("a"))

	next, cmd := m.Update(key("enter")) // the table answers with Selected
	m = next.(tuiModel)
	next, cmd = m.Update(cmd())
	m = next.(tuiModel)
	if !strings.Contains(m.status, "checking target") || cmd == nil {
		t.Fatalf("enter must start the checks, status %q", m.status)
	}
	gen := m.syncGen
	m, _ = pump(m, key("enter"))
	m, _ = pump(m, key("r"))
	if m.syncGen != gen || m.loading {
		t.Error("a running sync must ignore enter and r")
	}
	m, _ = pump(m, key("esc"))
	if m.stage != stageJobs {
		t.Error("a running sync must ignore esc")
	}

	next, cmd = m.Update(cmd()) // the plan arrives, the first job starts
	m = next.(tuiModel)
	if !strings.Contains(m.status, "1 of 3") || !strings.Contains(m.status, "server.rb") {
		t.Errorf("status must name the running job, got %q", m.status)
	}
	if !strings.Contains(m.View(), "ctrl+c") {
		t.Error("key hints must switch while a sync runs")
	}
	next, _ = m.Update(cmd())
	m = next.(tuiModel)
	if !strings.Contains(m.status, "2 of 3") || m.changesCell(m.program.Jobs[0]) != "synced –" {
		t.Errorf("the first outcome must show before the second job ends: %q", m.status)
	}
}

// An unavailable target aborts before anything runs — the real plan runs
// here, against an unmounted test target, with no ssh involved.
func TestTUISyncAbortsOnUnavailableTarget(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter"))
	ran := false
	m.runSyncJob = func(*twin.Config, *twin.Job, bool) jobOutcome { ran = true; return jobOutcome{} }
	m, _ = pump(m, key("enter"))
	if ran || m.run != nil || m.loading {
		t.Fatal("nothing may run or reload when the target is unavailable")
	}
	if !m.statusErr || !strings.Contains(m.status, "/t is not a mounted volume") {
		t.Errorf("status must name the unavailable target, got %q", m.status)
	}
}

// Target-side changes stop the sync before the first byte; the question is
// asked in the preview pane. n aborts, y overwrites, d shows the diffs.
func TestTUISyncConflictQuestion(t *testing.T) {
	m := loaded(t)
	m, _ = pump(m, key("enter"))
	job := m.program.Jobs[0]
	remote := &twin.Job{Path: "server.rb", Target: "book:/srv"}
	conflicts := []*twin.Entry{{Job: remote, Rel: "server.rb"}}
	calls := stubSync(&m, conflicts, nil)

	m, _ = pump(m, key("enter"))
	if m.run == nil || !m.run.asking || len(*calls) != 0 {
		t.Fatal("conflicts must hold the sync at the question")
	}
	view := m.View()
	for _, want := range []string{"target has changes of its own", "! server.rb", "overwrite and sync"} {
		if !strings.Contains(view, want) {
			t.Errorf("conflict question lacks %q:\n%s", want, view)
		}
	}
	m, _ = pump(m, key("d"))
	if !strings.Contains(m.View(), "no diff available") {
		t.Errorf("d must show the diffs in the preview:\n%s", m.View())
	}
	m, _ = pump(m, key("j"))
	if m.currentJob() != job {
		t.Error("the question holds the cursor")
	}
	m, _ = pump(m, key("n"))
	if m.run != nil || len(*calls) != 0 || !strings.Contains(m.status, "nothing was synced") {
		t.Fatalf("n must abort without syncing, status %q", m.status)
	}
	if strings.Contains(m.View(), "target has changes of its own") {
		t.Error("after n the preview must return to the job")
	}

	m, _ = pump(m, key("enter"))
	m, _ = pump(m, key("y"))
	if len(*calls) != 1 || !(*calls)[0].force {
		t.Errorf("y must sync with force: %+v", *calls)
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
	m = syncWith(m, m.program, jobOutcome{job: job, ok: true, transferred: true, output: ">f+++++++++ a\n>f.s....... b\n"})
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
	beta := m.programs[1].Jobs[0]
	m = syncWith(m, m.programs[1], jobOutcome{job: beta, ok: true})
	if _, kept := m.drifts[jobKey(beta)]; kept {
		t.Error("beta was synced; its verdict must be dropped so it is verified anew")
	}
	// Stay in stage 1: reopening beta would verify it with a real rsync.
	m.reenter = ""
	m, _ = pump(m, programsMsg{gen: m.loadGen, programs: dirPrograms()})
	if got := m.programs[0].Status(); got != twin.StatusInSync {
		t.Errorf("alpha's verdict must survive the reload, got %s", got)
	}
	if got := m.programs[1].Status(); got != twin.StatusUnverified {
		t.Errorf("beta was synced and must be verified anew, got %s", got)
	}
}
