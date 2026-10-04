package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rhsev/matterbase/basekit/frame"
	"github.com/rhsev/matterbase/basekit/input"
	previewpane "github.com/rhsev/matterbase/basekit/preview"
	"github.com/rhsev/matterbase/basekit/recordtable"
	"github.com/rhsev/matterbase/basekit/theme"

	"github.com/rhsev/mark-twin/internal/twin"
)

// The picker, on basekit: stage 1 lists the merged programs, stage 2 the
// jobs of one program with a multi-select column. The preview pane shows
// the program's contents in stage 1 and the rendered sync-file excerpt in
// stage 2. Directory jobs are verified when their program opens, and the
// rows update as rsync answers. Syncing runs in the background too: the
// status line follows it job by job, a conflict question is asked in the
// preview pane, and stage 2 reloads with fresh statuses at the end.

type stage int

const (
	stagePrograms stage = iota
	stageJobs
)

type tuiModel struct {
	cfg    *twin.Config
	file   string
	render renderer
	// load is loadCmd; tests swap in a stub so no grubber runs. planSync and
	// runSyncJob are the sync's two steps, swapped likewise so no ssh, rsync
	// or journal write happens in a test.
	load       func(cfg *twin.Config, file string, gen int) tea.Cmd
	planSync   func(cfg *twin.Config, jobs []*twin.Job) syncPlanMsg
	runSyncJob func(cfg *twin.Config, job *twin.Job, force bool) jobOutcome

	programs      []*twin.Program
	shownPrograms []*twin.Program
	programTable  recordtable.Model
	tableReady    bool

	program   *twin.Program
	shownJobs []*twin.Job
	selected  map[*twin.Job]bool
	// Evidence per job, keyed by jobKey so it survives reloads: dry-run
	// verdicts, sync outcomes, and when a drift verdict arrived.
	dryRuns     map[string]dryRunResult
	syncResults map[string]syncResult
	drifts      map[string]*twin.Drift
	driftAt     map[string]time.Time
	lastSync    string
	jobTable    recordtable.Model

	stage         stage
	filter        input.Model
	filterFocused bool
	prev          previewpane.Model

	loadGen, verifyGen, previewGen, dryRunGen int
	loading                                   bool
	verifying                                 int
	reenter                                   string
	previewCache                              map[string]string
	previewWant                               string
	previewJob                                *twin.Job

	// run is the sync in flight, nil when none; syncGen tags its messages.
	run     *syncRun
	syncGen int

	status    string
	statusErr bool
	width     int
	height    int
}

// syncRun is one sync from the first check to the last job: the jobs whose
// target is available, a pending conflict question, and the outcomes so far.
// Jobs run one after another, as in `twin sync`.
type syncRun struct {
	program   *twin.Program
	jobs      []*twin.Job
	conflicts []*twin.Entry
	asking    bool   // waiting for y / d / n
	diff      string // rendered once d was pressed
	force     bool
	outcomes  []jobOutcome
}

func newTUI(cfg *twin.Config, file string, render renderer) tuiModel {
	// The first load is generation 1 from the start: Init runs on a copy of
	// the model, so anything it mutated would be lost — and a result whose
	// generation the real model never saw is dropped as stale.
	m := tuiModel{
		cfg: cfg, file: file, render: render, load: loadCmd,
		planSync: planSync, runSyncJob: runSyncJob,
		loadGen: 1, loading: true, status: "loading sync-files…",
		selected:     map[*twin.Job]bool{},
		dryRuns:      map[string]dryRunResult{},
		syncResults:  map[string]syncResult{},
		drifts:       map[string]*twin.Drift{},
		driftAt:      map[string]time.Time{},
		previewCache: map[string]string{},
	}
	m.filter = input.New(input.Config{Placeholder: "filter…"})
	m.programTable = recordtable.New(recordtable.Config{Columns: programColumns})
	m.jobTable = recordtable.New(recordtable.Config{Columns: jobColumns})
	m.prev = previewpane.New()
	return m
}

func runTUI(cfg *twin.Config, file string) error {
	m := newTUI(cfg, file, makeRenderer(cfg))
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// Init only issues the command; it must not mutate m (value receiver).
func (m tuiModel) Init() tea.Cmd {
	return m.load(m.cfg, m.file, m.loadGen)
}

// ── background work ──────────────────────────────────────────────────────────

func (m *tuiModel) startLoad() tea.Cmd {
	m.loadGen++
	m.loading = true
	m.setStatus("loading sync-files…", false)
	return m.load(m.cfg, m.file, m.loadGen)
}

// startVerify asks rsync about every unverified directory job among jobs;
// rows update as answers arrive.
func (m *tuiModel) startVerify(jobs []*twin.Job) tea.Cmd {
	candidates := twin.DriftCandidates(jobs)
	if len(candidates) == 0 {
		return nil
	}
	m.verifyGen++
	m.verifying += len(candidates)
	cmds := make([]tea.Cmd, len(candidates))
	for i, j := range candidates {
		cmds[i] = driftCmd(m.cfg, j, m.verifyGen)
	}
	return tea.Batch(cmds...)
}

// ── state transitions ────────────────────────────────────────────────────────

func (m *tuiModel) applyPrograms(programs []*twin.Program) tea.Cmd {
	m.programs = programs
	m.previewCache = map[string]string{}
	m.reattachDrifts()
	if !m.tableReady {
		m.programTable = recordtable.New(recordtable.Config{Columns: programColumns, Widths: programWidths(programs)})
		m.tableReady = true
		m.applyLayout()
	}
	m.refreshProgramRows()

	if m.reenter != "" {
		name := m.reenter
		m.reenter = ""
		if p := findProgram(programs, name); p != nil {
			return m.reopenProgram(p)
		}
		m.leaveProgram()
	}
	m.updateSummary()
	return m.updatePreview()
}

func (m *tuiModel) refreshProgramRows() {
	m.shownPrograms = filterPrograms(m.programs, m.filterTerm(stagePrograms))
	m.programTable.SetRecords(programRecords(m.shownPrograms))
}

func (m *tuiModel) refreshJobRows() {
	if m.program == nil {
		return
	}
	m.shownJobs = filterJobs(m.program.Jobs, m.filterTerm(stageJobs))
	m.jobTable.SetRecords(jobRecords(m.program, m.shownJobs, m.selected, m.changesCell))
}

// filterTerm is the filter text when it applies to the given stage.
func (m *tuiModel) filterTerm(s stage) string {
	if m.stage != s {
		return ""
	}
	return m.filter.Value()
}

// enterProgram opens stage 2 for p with a fresh table sized to its jobs.
func (m *tuiModel) enterProgram(p *twin.Program) tea.Cmd {
	m.program = p
	m.stage = stageJobs
	m.selected = map[*twin.Job]bool{}
	m.filter.SetValue("")
	m.jobTable = recordtable.New(recordtable.Config{Columns: jobColumns, Widths: jobWidths(p, p.Jobs)})
	m.applyLayout()
	m.refreshJobRows()
	verify := m.startVerify(p.Jobs)
	m.updateSummary()
	return tea.Batch(verify, m.updatePreview())
}

// reopenProgram re-enters stage 2 after a reload: same table, so the
// cursor stays where it was.
func (m *tuiModel) reopenProgram(p *twin.Program) tea.Cmd {
	m.program = p
	m.stage = stageJobs
	m.selected = map[*twin.Job]bool{}
	m.refreshJobRows()
	verify := m.startVerify(p.Jobs)
	m.updateSummary()
	return tea.Batch(verify, m.updatePreview())
}

func (m *tuiModel) leaveProgram() {
	m.stage = stagePrograms
	m.program = nil
	m.lastSync = ""
	m.verifyGen++
	m.verifying = 0
	m.filter.SetValue("")
	m.refreshProgramRows()
	m.updateSummary()
}

// currentProgram is the program under the stage-1 cursor.
func (m *tuiModel) currentProgram() *twin.Program {
	_, i, ok := m.programTable.Current()
	if !ok || i >= len(m.shownPrograms) {
		return nil
	}
	return m.shownPrograms[i]
}

// currentJob is the job under the stage-2 cursor.
func (m *tuiModel) currentJob() *twin.Job {
	_, i, ok := m.jobTable.Current()
	if !ok || i >= len(m.shownJobs) {
		return nil
	}
	return m.shownJobs[i]
}

// selectedJobs are the toggled jobs in document order, or the current
// row when nothing is toggled.
func (m *tuiModel) selectedJobs() []*twin.Job {
	var out []*twin.Job
	for _, j := range m.program.Jobs {
		if m.selected[j] {
			out = append(out, j)
		}
	}
	if len(out) == 0 {
		if j := m.currentJob(); j != nil {
			out = append(out, j)
		}
	}
	return out
}

func (m *tuiModel) toggleCurrent() {
	j := m.currentJob()
	if j == nil {
		return
	}
	if m.selected[j] {
		delete(m.selected, j)
	} else {
		m.selected[j] = true
	}
	m.refreshJobRows()
	m.updateSummary()
}

func (m *tuiModel) selectAll(on bool) {
	for _, j := range m.shownJobs {
		if on {
			m.selected[j] = true
		} else {
			delete(m.selected, j)
		}
	}
	m.refreshJobRows()
	m.updateSummary()
}

// sync starts a sync of the selected jobs: first the checks before the
// first byte (availability, conflicts), then the jobs one by one.
func (m *tuiModel) sync() tea.Cmd {
	if m.program == nil || m.run != nil {
		return nil
	}
	jobs := m.selectedJobs()
	if len(jobs) == 0 {
		return nil
	}
	m.syncGen++
	m.run = &syncRun{program: m.program}
	m.setStatus(fmt.Sprintf("sync %s: checking target…", m.program.Name), false)
	return syncPlanCmd(m.planSync, m.cfg, jobs, m.syncGen)
}

// applySyncPlan acts on the checks: abort on an unavailable target, ask
// about conflicts, or start the first job.
func (m *tuiModel) applySyncPlan(msg syncPlanMsg) tea.Cmd {
	run := m.run
	switch {
	case msg.err != nil:
		m.run = nil
		m.setStatus("sync: "+msg.err.Error(), true)
		return nil
	case len(msg.reasons) > 0:
		m.run = nil
		m.setStatus("abort: "+strings.Join(msg.reasons, "; ")+" — nothing was synced", true)
		return nil
	case len(msg.ready) == 0:
		m.run = nil
		m.setStatus("sync: no active job selected", false)
		return nil
	}
	run.jobs = msg.ready
	if len(msg.conflicts) > 0 {
		run.conflicts = msg.conflicts
		run.asking = true
		m.setStatus(fmt.Sprintf("target has %d changed file(s) — y overwrite and sync · d diff · n abort",
			len(msg.conflicts)), true)
		m.showPreviewPane()
		return m.updatePreview()
	}
	return m.nextSyncJob()
}

// answerConflict handles a key while the conflict question is open.
func (m *tuiModel) answerConflict(key string) tea.Cmd {
	run := m.run
	switch key {
	case "y":
		run.asking = false
		run.force = true
		return tea.Batch(m.nextSyncJob(), m.updatePreview())
	case "n", "esc":
		m.run = nil
		m.setStatus("aborted — nothing was synced", false)
		return m.updatePreview()
	case "d":
		if run.diff == "" {
			run.diff = theme.Label.Render("rendering diff…")
			m.updatePreview()
			return conflictDiffCmd(run.conflicts, m.syncGen)
		}
	}
	return nil
}

// nextSyncJob starts the next job, or finishes the run and reloads.
func (m *tuiModel) nextSyncJob() tea.Cmd {
	run := m.run
	i := len(run.outcomes)
	if i < len(run.jobs) {
		job := run.jobs[i]
		m.setStatus(fmt.Sprintf("sync %s: %d of %d · %s…", run.program.Name, i+1, len(run.jobs), job.Path), false)
		return syncJobCmd(m.runSyncJob, m.cfg, job, run.force, m.syncGen)
	}
	m.lastSync = syncSummary(run.outcomes)
	m.run = nil
	m.reenter = run.program.Name
	return m.startLoad()
}

// showPreviewPane opens the preview pane if it was toggled off.
func (m *tuiModel) showPreviewPane() {
	if !m.prev.Visible() {
		m.prev.Toggle()
		m.applyLayout()
	}
}

// conflictPreview is the conflict question in the preview pane: the
// changed files, the keys, and the diffs once asked for.
func (m *tuiModel) conflictPreview() string {
	run := m.run
	var b strings.Builder
	b.WriteString(theme.Title.Render("target has changes of its own") + "\n")
	b.WriteString(indentStyled(conflictReport(run.conflicts), textStyle))
	b.WriteString("\n" + footerKeyStyle.Render("y") + " " + footerDescStyle.Render("overwrite and sync") + "  " +
		footerKeyStyle.Render("d") + " " + footerDescStyle.Render("diff") + "  " +
		footerKeyStyle.Render("n") + " " + footerDescStyle.Render("abort, sync nothing") + "\n")
	if run.diff != "" {
		b.WriteString("\n" + run.diff)
	}
	return b.String()
}

// dryRun previews the selected jobs inside the TUI; the report replaces
// the excerpt in the preview pane until the cursor moves.
func (m *tuiModel) dryRun() tea.Cmd {
	if m.program == nil {
		return nil
	}
	jobs := m.selectedJobs()
	if len(jobs) == 0 {
		return nil
	}
	m.dryRunGen++
	m.setStatus(fmt.Sprintf("dry-run: %d job(s)…", len(jobs)), false)
	return dryRunCmd(m.cfg, m.program, jobs, m.dryRunGen)
}

// showDryRun attaches the verdicts to their jobs — table column and
// preview section — and summarises them in the status line.
func (m *tuiModel) showDryRun(msg dryRunMsg) tea.Cmd {
	changed := 0
	now := time.Now()
	for _, r := range msg.results {
		r.at = now
		m.dryRuns[jobKey(r.job)] = r
		if r.changes > 0 {
			changed++
		}
	}
	m.showPreviewPane()
	m.refreshJobRows()
	verdict := fmt.Sprintf("dry-run: %d of %d job(s) would change", changed, len(msg.results))
	if changed == 0 {
		verdict = fmt.Sprintf("dry-run: nothing would change (%d job(s))", len(msg.results))
	}
	m.setStatus(verdict, false)
	return m.updatePreview()
}

// composeJobPreview stacks the job's most recent verdict (a sync outcome
// or a dry run, whichever came last) above its rendered excerpt.
func (m *tuiModel) composeJobPreview(j *twin.Job, excerpt string) string {
	key := jobKey(j)
	dr, hasDry := m.dryRuns[key]
	sr, hasSync := m.syncResults[key]
	switch {
	case hasSync && (!hasDry || sr.at.After(dr.at)):
		body := sr.output
		if sr.ok {
			body = twin.Summarize(sr.output)
		}
		return theme.Title.Render(syncHeading(sr)) + "\n" + indentStyled(body, textStyle) + "\n" + excerpt
	case hasDry:
		return theme.Title.Render(dryRunHeading(dr)) + "\n" + dr.report + "\n" + excerpt
	}
	return excerpt
}

// ── status / preview ─────────────────────────────────────────────────────────

func (m *tuiModel) setStatus(s string, isErr bool) {
	m.status, m.statusErr = s, isErr
}

func (m *tuiModel) updateSummary() {
	var s string
	switch m.stage {
	case stagePrograms:
		s = fmt.Sprintf("%d programs", len(m.shownPrograms))
		if len(m.shownPrograms) != len(m.programs) {
			s += fmt.Sprintf(" of %d", len(m.programs))
		}
		if n := needsAttention(m.programs); n > 0 {
			s += fmt.Sprintf(" · %d need attention", n)
		}
	case stageJobs:
		n := 0
		for _, j := range m.program.Jobs {
			if m.selected[j] {
				n++
			}
		}
		s = fmt.Sprintf("%s · %d of %d selected", m.program.Name, n, len(m.program.Jobs))
		if m.lastSync != "" {
			s += " · " + m.lastSync
		}
	}
	if m.loading {
		s += " · loading…"
	}
	if m.verifying > 0 {
		s += fmt.Sprintf(" · verifying %d…", m.verifying)
	}
	m.setStatus(s, false)
}

func (m *tuiModel) previewWidth() int {
	s := m.sizes()
	return frame.Inner(s.PreviewWidth)
}

// updatePreview fills the pane for the current row; stage-2 excerpts are
// rendered off the loop and cached per job and width.
func (m *tuiModel) updatePreview() tea.Cmd {
	if !m.prev.Visible() {
		return nil
	}
	if m.run != nil && m.run.asking {
		m.prev.SetTitle(m.run.program.Name, "conflicts")
		m.prev.SetContent(m.conflictPreview())
		return nil
	}
	switch m.stage {
	case stagePrograms:
		p := m.currentProgram()
		if p == nil {
			m.prev.SetTitle("", "")
			m.prev.SetContent("")
			return nil
		}
		m.prev.SetTitle(p.Name, "program")
		m.prev.SetContent(programOverview(p, m.previewWidth()))
		return nil
	case stageJobs:
		j := m.currentJob()
		if j == nil {
			m.prev.SetTitle("", "")
			m.prev.SetContent("")
			return nil
		}
		width := m.previewWidth()
		m.prev.SetTitle(filepath.Base(j.SyncFile), j.Path)
		key := j.SyncFile + "\x00" + j.Path + "\x00" + strconv.Itoa(width)
		m.previewWant, m.previewJob = key, j
		if ansi, ok := m.previewCache[key]; ok {
			m.prev.SetContent(m.composeJobPreview(j, ansi))
			return nil
		}
		m.prev.SetContent(m.composeJobPreview(j, theme.Label.Render("rendering…")))
		m.previewGen++
		return previewCmd(m.render, j, width, key, m.previewGen)
	}
	return nil
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.applyLayout()
		return m, tea.Batch(tea.ClearScreen, m.updatePreview())

	case programsMsg:
		if msg.gen != m.loadGen {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.setStatus("error: "+msg.err.Error(), true)
			m.reenter = ""
			return m, nil
		}
		return m, m.applyPrograms(msg.programs)

	case driftMsg:
		if msg.gen != m.verifyGen {
			return m, nil
		}
		m.verifying = max(m.verifying-1, 0)
		if msg.err != nil {
			m.setStatus("verify: "+msg.err.Error(), true)
			return m, nil
		}
		msg.job.Drift = msg.drift
		m.drifts[jobKey(msg.job)] = msg.drift
		m.driftAt[jobKey(msg.job)] = time.Now()
		m.refreshProgramRows()
		m.refreshJobRows()
		m.updateSummary()
		return m, m.updatePreview()

	case previewMsg:
		m.previewCache[msg.key] = msg.ansi
		if msg.key == m.previewWant && m.previewJob != nil {
			m.prev.SetContent(m.composeJobPreview(m.previewJob, msg.ansi))
		}
		return m, nil

	case syncPlanMsg:
		if msg.gen != m.syncGen || m.run == nil {
			return m, nil
		}
		return m, m.applySyncPlan(msg)

	case syncJobMsg:
		if msg.gen != m.syncGen || m.run == nil {
			return m, nil
		}
		m.run.outcomes = append(m.run.outcomes, msg.outcome)
		m.recordOutcome(msg.outcome, time.Now())
		m.refreshJobRows()
		return m, tea.Batch(m.nextSyncJob(), m.updatePreview())

	case conflictDiffMsg:
		if msg.gen != m.syncGen || m.run == nil || !m.run.asking {
			return m, nil
		}
		m.run.diff = msg.text
		return m, m.updatePreview()

	case dryRunMsg:
		if msg.gen != m.dryRunGen || msg.program != m.program {
			return m, nil
		}
		return m, m.showDryRun(msg)

	case recordtable.Highlighted:
		return m, m.updatePreview()

	case recordtable.Selected:
		switch m.stage {
		case stagePrograms:
			if p := m.currentProgram(); p != nil {
				return m, m.enterProgram(p)
			}
		case stageJobs:
			return m, m.sync()
		}
		return m, nil

	case input.Changed:
		m.refreshProgramRows()
		m.refreshJobRows()
		m.updateSummary()
		return m, m.updatePreview()

	case input.Submitted:
		m.setFilterFocused(false)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *tuiModel) setFilterFocused(on bool) tea.Cmd {
	m.filterFocused = on
	if on {
		m.programTable.Blur()
		m.jobTable.Blur()
		return m.filter.Focus()
	}
	m.filter.Blur()
	m.programTable.Focus()
	m.jobTable.Focus()
	return nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	if m.filterFocused {
		switch key {
		case "esc":
			m.filter.SetValue("")
			m.setFilterFocused(false)
			m.refreshProgramRows()
			m.refreshJobRows()
			m.updateSummary()
			return m, m.updatePreview()
		case "tab":
			m.setFilterFocused(false)
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}

	// A sync in flight owns the keyboard: the conflict question takes its
	// answer, and while jobs run only moving around is allowed — no second
	// sync, no reload that would replace the jobs under it.
	if m.run != nil {
		switch {
		case m.run.asking && key == "q":
			return m, tea.Quit // nothing has been synced yet
		case m.run.asking && (key == "y" || key == "n" || key == "d" || key == "esc"):
			return m, m.answerConflict(key)
		case key == "K" || key == "J" || key == "p":
			// preview scrolling and toggling, handled below
		case m.run.asking:
			return m, nil
		case key == "up" || key == "down" || key == "k" || key == "j" ||
			key == "pgup" || key == "pgdown" || key == "home" || key == "end":
			var cmd tea.Cmd
			m.jobTable, cmd = m.jobTable.Update(msg)
			return m, cmd
		default:
			return m, nil
		}
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "/":
		return m, m.setFilterFocused(true)
	case "p":
		m.prev.Toggle()
		m.applyLayout()
		return m, m.updatePreview()
	case "K", "J":
		// Scroll the preview without moving the table cursor.
		dir := tea.KeyUp
		if key == "J" {
			dir = tea.KeyDown
		}
		var cmd tea.Cmd
		m.prev, cmd = m.prev.Update(tea.KeyMsg{Type: dir})
		return m, cmd
	case "r":
		if m.program != nil {
			m.reenter = m.program.Name
		}
		return m, m.startLoad()
	}

	switch m.stage {
	case stagePrograms:
		switch key {
		case "esc":
			return m, tea.Quit
		case "v":
			var jobs []*twin.Job
			for _, p := range m.programs {
				jobs = append(jobs, p.Jobs...)
			}
			cmd := m.startVerify(jobs)
			m.updateSummary()
			return m, cmd
		}
		var cmd tea.Cmd
		m.programTable, cmd = m.programTable.Update(msg)
		return m, cmd

	case stageJobs:
		switch key {
		case "esc":
			m.leaveProgram()
			return m, m.updatePreview()
		case " ", "tab":
			m.toggleCurrent()
			return m, nil
		case "a", "ctrl+a":
			m.selectAll(true)
			return m, nil
		case "u", "ctrl+u":
			m.selectAll(false)
			return m, nil
		case "d":
			return m, m.dryRun()
		case "v":
			for _, j := range m.program.Jobs {
				if j.Directory {
					j.Drift = nil
					delete(m.drifts, jobKey(j))
					delete(m.driftAt, jobKey(j))
				}
			}
			m.refreshJobRows()
			cmd := m.startVerify(m.program.Jobs)
			m.updateSummary()
			return m, cmd
		}
		var cmd tea.Cmd
		m.jobTable, cmd = m.jobTable.Update(msg)
		return m, cmd
	}
	return m, nil
}

// ── layout / View ────────────────────────────────────────────────────────────

func (m *tuiModel) sizes() frame.Sizes {
	return frame.Compute(frame.Config{
		ShowTopbar: true, TopbarHeight: 3,
		ShowPreview: m.prev.Visible(), PreviewPct: 0.5, PreviewMax: 100,
		StatusHeight: 2,
	}, m.width, m.height)
}

func (m *tuiModel) applyLayout() {
	if m.width == 0 {
		return
	}
	s := m.sizes()
	m.filter.SetWidth(s.Width)
	m.programTable.SetSize(frame.Inner(s.TableWidth), frame.Inner(s.MainHeight))
	m.jobTable.SetSize(frame.Inner(s.TableWidth), frame.Inner(s.MainHeight))
	if s.PreviewWidth > 0 {
		m.prev.SetSize(frame.Inner(s.PreviewWidth), frame.Inner(s.MainHeight))
	}
}

func clampWidth(s string, width int) string {
	return lipgloss.NewStyle().MaxWidth(width).Render(frame.Sanitize(s))
}

func clampBox(s string, width, height int) string {
	return lipgloss.NewStyle().MaxWidth(width).MaxHeight(max(height, 1)).Render(frame.Sanitize(s))
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return ""
	}
	s := m.sizes()

	topbar := m.filter.View()

	table := m.programTable
	if m.stage == stageJobs {
		table = m.jobTable
	}
	pane := theme.PaneFocused
	if m.filterFocused {
		pane = theme.Pane
	}
	tableBox := pane.Width(s.TableWidth - 2).Height(s.MainHeight - 2).
		Render(clampBox(colorizeRows(table.View()), s.TableWidth-2, s.MainHeight-2))

	var previewBox string
	if m.prev.Visible() && s.PreviewWidth > 0 {
		previewBox = theme.Pane.Width(s.PreviewWidth - 2).Height(s.MainHeight - 2).
			Render(clampBox(m.prev.View(), s.PreviewWidth-2, s.MainHeight-2))
	}
	main := frame.JoinRow("", tableBox, previewBox)

	statusStyle := theme.StatusBar
	if m.statusErr {
		statusStyle = theme.StatusError
	}
	statusLine := statusStyle.Width(s.Width).Render(clampWidth(m.status, s.Width-2))
	hintLine := footerHintStyle.Width(s.Width).Render(clampWidth(m.keyHints(), s.Width-2))

	return frame.JoinColumn(topbar, main, lipgloss.JoinVertical(lipgloss.Left, statusLine, hintLine))
}

var (
	footerHintStyle = lipgloss.NewStyle().Padding(0, 1)
	footerKeyStyle  = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	footerDescStyle = lipgloss.NewStyle().Foreground(theme.Muted)
)

type hint struct{ key, desc string }

var (
	programHints = []hint{
		{"enter", "open"}, {"/", "filter"}, {"v", "verify all"}, {"r", "reload"},
		{"p", "preview"}, {"J/K", "scroll"}, {"q", "quit"},
	}
	jobHints = []hint{
		{"space", "toggle"}, {"a", "all"}, {"u", "none"}, {"enter", "sync"},
		{"d", "dry-run → preview"}, {"esc", "back"}, {"/", "filter"}, {"v", "re-verify"},
		{"p", "preview"}, {"q", "quit"},
	}
	conflictHints = []hint{
		{"y", "overwrite and sync"}, {"d", "diff"}, {"n/esc", "abort"}, {"J/K", "scroll"}, {"q", "quit"},
	}
	syncingHints = []hint{
		{"↑/↓", "move"}, {"J/K", "scroll"}, {"p", "preview"}, {"ctrl+c", "quit"},
	}
)

func (m tuiModel) keyHints() string {
	hints := programHints
	switch {
	case m.run != nil && m.run.asking:
		hints = conflictHints
	case m.run != nil:
		hints = syncingHints
	case m.stage == stageJobs:
		hints = jobHints
	}
	parts := make([]string, len(hints))
	for i, h := range hints {
		parts[i] = footerKeyStyle.Render(h.key) + " " + footerDescStyle.Render(h.desc)
	}
	return strings.Join(parts, "  ")
}

// pickAndSync is the interactive entry point. Without a terminal there is
// nothing to draw on — say so and point at the commands that work
// unattended (cron, `ssh host twin …`, an agent shell).
func pickAndSync(cfg *twin.Config, file string) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		hint := ""
		if file != "" {
			hint = " --file=" + file
		}
		fmt.Fprintln(os.Stderr, "twin: the TUI needs a terminal.")
		fmt.Fprintf(os.Stderr, "      use `twin sync%s` for an unattended run (add --dry-run to preview).\n", hint)
		return errExit
	}
	return runTUI(cfg, file)
}

// recordOutcome keeps what the sync did to a job, so the row and the
// preview show it at once and after the reload; a dry-run verdict for the
// same job is superseded.
func (m *tuiModel) recordOutcome(o jobOutcome, at time.Time) {
	r := syncResult{ok: o.ok, transferred: o.transferred, output: o.output, at: at}
	if o.transferred {
		r.changes = len(twin.ParseItemized(o.output))
	}
	key := jobKey(o.job)
	m.syncResults[key] = r
	delete(m.dryRuns, key)
	// The verdict from before the sync is stale; the reopen re-verifies.
	delete(m.drifts, key)
	delete(m.driftAt, key)
}

// syncSummary is the run's verdict for the status line.
func syncSummary(outcomes []jobOutcome) string {
	changed, failed := 0, 0
	for _, o := range outcomes {
		if o.transferred {
			changed++
		}
		if !o.ok {
			failed++
		}
	}
	s := fmt.Sprintf("synced %d of %d changed", changed, len(outcomes))
	if failed > 0 {
		s += fmt.Sprintf(", %d failed", failed)
	}
	return s
}

// changesCell says what a sync would move (or just did), from the most
// recent evidence: a sync outcome, a dry run, or a drift verdict, whichever
// came last; a file's mtime status is the fallback, blank means unknown.
func (m *tuiModel) changesCell(j *twin.Job) string {
	key := jobKey(j)
	var latest time.Time
	cell := statusCell(j)
	if j.Drift != nil {
		latest = m.driftAt[key]
		cell = driftCell(j.Drift)
	}
	if r, ok := m.dryRuns[key]; ok && !r.at.Before(latest) {
		latest = r.at
		switch {
		case r.skipped:
			cell = "skipped"
		case !r.ok:
			cell = "error"
		case r.changes == 0:
			cell = "– (dry-run)"
		default:
			cell = fmt.Sprintf("%d (dry-run)", r.changes)
		}
	}
	if r, ok := m.syncResults[key]; ok && !r.at.Before(latest) {
		switch {
		case !r.ok:
			cell = "sync failed"
		case !r.transferred:
			cell = "synced –"
		default:
			cell = fmt.Sprintf("synced %d", r.changes)
		}
	}
	return cell
}

// reattachDrifts puts the verification verdicts back onto the freshly
// loaded job objects: a reload changes the objects, not the facts rsync
// established. Jobs synced since have had their verdict dropped and get
// verified anew.
func (m *tuiModel) reattachDrifts() {
	for _, p := range m.programs {
		for _, j := range p.Jobs {
			if !j.Directory || j.Drift != nil {
				continue
			}
			if d, ok := m.drifts[jobKey(j)]; ok {
				j.Drift = d
			}
		}
	}
}
