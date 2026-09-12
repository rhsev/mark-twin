package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rhsev/matterbase/basekit/theme"

	twin "github.com/rhsev/mark-twin"
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

type syncDoneMsg struct {
	quit     bool
	outcomes []jobOutcome
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

// syncExec hands the terminal back to the CLI sync path — output, journal,
// the conflict prompt with its diff view — exactly as `twin sync` does,
// then waits for Enter the way the fzf picker did. Implements
// tea.ExecCommand; the stdio setters are no-ops because syncJobs writes
// to the process stdio directly.
type syncExec struct {
	cfg      *twin.Config
	program  *twin.Program
	jobs     []*twin.Job
	quit     bool
	outcomes []jobOutcome
}

func (s *syncExec) Run() error {
	fmt.Println()
	_, outcomes, err := syncJobs(s.cfg, s.program, s.jobs, syncOpts{})
	s.outcomes = outcomes
	if err != nil && !errors.Is(err, errExit) {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	fmt.Print("\npress Enter to continue, q to quit ")
	line, ok := readLine()
	s.quit = ok && line == "q"
	return nil
}

func (s *syncExec) SetStdin(io.Reader)  {}
func (s *syncExec) SetStdout(io.Writer) {}
func (s *syncExec) SetStderr(io.Writer) {}

func syncCmd(s *syncExec) tea.Cmd {
	return tea.Exec(s, func(error) tea.Msg { return syncDoneMsg{quit: s.quit, outcomes: s.outcomes} })
}
