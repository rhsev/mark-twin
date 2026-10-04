package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rhsev/matterbase/basekit/theme"

	"github.com/rhsev/mark-twin/internal/twin"
)

// Messages the TUI's background work reports back with. Every message
// carries the generation it was started under, so a reload or a stage
// change makes late results fall on the floor instead of on the wrong
// rows.

type programsMsg struct {
	gen      int
	programs []*twin.Program
	err      error
}

type driftMsg struct {
	gen   int
	job   *twin.Job
	drift *twin.Drift
	err   error
}

type previewMsg struct {
	gen  int
	key  string
	ansi string
}

// syncPlanMsg is what a sync learned before the first byte moves: which
// targets are available and what the target changed on its own.
type syncPlanMsg struct {
	gen       int
	ready     []*twin.Job
	reasons   []string
	conflicts []*twin.Entry
	err       error
}

// syncJobMsg is one job's outcome; the next job starts when it arrives.
type syncJobMsg struct {
	gen     int
	outcome jobOutcome
}

// conflictDiffMsg carries the rendered diffs for the conflict question.
type conflictDiffMsg struct {
	gen  int
	text string
}

// syncResult is what a sync did to one job, kept across reloads.
type syncResult struct {
	ok          bool
	transferred bool
	changes     int
	output      string
	at          time.Time
}

// jobKey identifies a job across reloads, which create new Job objects.
func jobKey(j *twin.Job) string { return j.SyncFile + "\x00" + j.Program + "\x00" + j.Path }

// syncHeading is the one-line verdict above a job's sync output.
func syncHeading(r syncResult) string {
	switch {
	case !r.ok:
		return "sync failed"
	case !r.transferred:
		return "synced: nothing to transfer"
	case r.changes == 1:
		return "synced: 1 change"
	}
	return fmt.Sprintf("synced: %d changes", r.changes)
}

// dryRunResult is one job's dry-run verdict; it stays attached to the job
// (table column, preview section) until the next reload.
type dryRunResult struct {
	job     *twin.Job
	report  string // rendered verdict lines
	changes int    // itemize entries a sync would apply
	ok      bool
	skipped bool
	at      time.Time
}

// dryRunMsg is the outcome of an in-TUI dry run.
type dryRunMsg struct {
	gen     int
	program *twin.Program
	results []dryRunResult
}

// dryRunCmd runs the selected jobs with --dry-run off the main loop and
// renders one verdict per job. A dry run has no prompt and no journal, so
// nothing forces it out of the TUI.
func dryRunCmd(cfg *twin.Config, program *twin.Program, jobs []*twin.Job, gen int) tea.Cmd {
	return func() tea.Msg {
		results := make([]dryRunResult, 0, len(jobs))
		for _, job := range jobs {
			r := dryRunResult{job: job}
			if reason := targetAvailability(job); reason != "" {
				r.skipped = true
				r.report = warnStyle.Render("  skipped: "+reason) + "\n"
				results = append(results, r)
				continue
			}
			ok, out, _ := twin.RunJob(cfg, job, true, false)
			r.ok = ok
			switch {
			case !ok:
				r.report = indentStyled(out, theme.StatusError.UnsetPadding())
			case twin.Transferred(out):
				r.changes = len(twin.ParseItemized(out))
				r.report = indentStyled(twin.Summarize(out), textStyle)
			default:
				r.report = theme.Label.Render("  (nothing would change)") + "\n"
			}
			results = append(results, r)
		}
		return dryRunMsg{gen: gen, program: program, results: results}
	}
}

// dryRunHeading is the one-line verdict above a job's report.
func dryRunHeading(r dryRunResult) string {
	switch {
	case r.skipped:
		return "dry-run: skipped"
	case !r.ok:
		return "dry-run: failed"
	case r.changes == 0:
		return "dry-run: nothing would change"
	case r.changes == 1:
		return "dry-run: 1 change"
	}
	return fmt.Sprintf("dry-run: %d changes", r.changes)
}

// indentStyled indents every non-empty line by two columns in style.
func indentStyled(text string, style lipgloss.Style) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(style.Render("  "+line) + "\n")
	}
	return b.String()
}

// loadCmd runs grubber and the remote stat round, then merges programs
// the way the picker shows them.
func loadCmd(cfg *twin.Config, file string, gen int) tea.Cmd {
	return func() tea.Msg {
		programs, err := twin.LoadPrograms(cfg, file, "", false)
		if err != nil {
			return programsMsg{gen: gen, err: err}
		}
		return programsMsg{gen: gen, programs: twin.MergePrograms(programs)}
	}
}

// driftSlots bounds concurrent rsync dry-runs — the same four the CLI's
// FillDrift uses — however many verifications are in flight.
var driftSlots = make(chan struct{}, 4)

// driftCmd asks rsync about one directory job.
func driftCmd(cfg *twin.Config, job *twin.Job, gen int) tea.Cmd {
	return func() tea.Msg {
		driftSlots <- struct{}{}
		defer func() { <-driftSlots }()
		d, err := twin.DetectDrift(cfg, job)
		return driftMsg{gen: gen, job: job, drift: d, err: err}
	}
}

// previewCmd renders one job's excerpt off the main loop.
func previewCmd(render renderer, job *twin.Job, width int, key string, gen int) tea.Cmd {
	return func() tea.Msg {
		return previewMsg{gen: gen, key: key, ansi: render(job, width)}
	}
}

// planSync runs the checks the CLI runs before syncing: target
// availability (ssh probe, mount check) and the conflict detection round.
// Both can take seconds, which is why they run off the main loop.
func planSync(cfg *twin.Config, jobs []*twin.Job) syncPlanMsg {
	var msg syncPlanMsg
	msg.ready, msg.reasons = partitionAvailable(activeJobs(jobs))
	if len(msg.reasons) > 0 || len(msg.ready) == 0 {
		return msg
	}
	msg.conflicts, msg.err = detectConflicts(cfg, msg.ready)
	return msg
}

// runSyncJob syncs one job and journals it, exactly as `twin sync` does.
func runSyncJob(cfg *twin.Config, job *twin.Job, force bool) jobOutcome {
	return runAndRecord(cfg, job, false, force)
}

func syncPlanCmd(plan func(*twin.Config, []*twin.Job) syncPlanMsg, cfg *twin.Config, jobs []*twin.Job, gen int) tea.Cmd {
	return func() tea.Msg {
		msg := plan(cfg, jobs)
		msg.gen = gen
		return msg
	}
}

func syncJobCmd(run func(*twin.Config, *twin.Job, bool) jobOutcome, cfg *twin.Config, job *twin.Job, force bool, gen int) tea.Cmd {
	return func() tea.Msg {
		return syncJobMsg{gen: gen, outcome: run(cfg, job, force)}
	}
}

// conflictDiffCmd renders the diffs for the conflict question; a diff runs
// a subprocess per file.
func conflictDiffCmd(conflicts []*twin.Entry, gen int) tea.Cmd {
	return func() tea.Msg {
		return conflictDiffMsg{gen: gen, text: conflictDiffs(conflicts)}
	}
}
