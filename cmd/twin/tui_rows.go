package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rhsev/matterbase/basekit/recordtable"

	twin "github.com/rhsev/mark-twin"
)

// Column keys double as header titles — recordtable renders the key.
const (
	colStatus  = "st"
	colProgram = "program"
	colJobs    = "jobs"
	colFiles   = "sync-files"
	colSel     = " "
	colPath    = "path"
	colDelta   = "Δ"
	colChanges = "changes"
	colFile    = "file"
)

var (
	programColumns = []string{colStatus, colProgram, colJobs, colFiles}
	jobColumns     = []string{colSel, colStatus, colPath, colDelta, colChanges, colFile}
)

// programRecords renders one table row per merged program.
func programRecords(programs []*twin.Program) []recordtable.Record {
	recs := make([]recordtable.Record, len(programs))
	for i, p := range programs {
		recs[i] = recordtable.Record{
			colStatus:  twin.Icon(p.Status()),
			colProgram: p.Name,
			colJobs:    fmt.Sprintf("%d/%d", len(p.ActiveJobs()), len(p.Jobs)),
			colFiles:   strings.Join(syncFileNames(p), " · "),
		}
	}
	return recs
}

// syncFileNames lists the distinct sync-file basenames of a program, in
// job order.
func syncFileNames(p *twin.Program) []string {
	var out []string
	seen := map[string]bool{}
	for _, j := range p.Jobs {
		name := filepath.Base(j.SyncFile)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// jobRecords renders one row per job; selected marks the toggled ones. A
// merged program spans sync-files (and bracketed variants), so the file
// column names each job's origin — what the fzf picker needed inert
// section rows for.
func jobRecords(program *twin.Program, jobs []*twin.Job, selected map[*twin.Job]bool, cell func(*twin.Job) string) []recordtable.Record {
	recs := make([]recordtable.Record, len(jobs))
	for i, j := range jobs {
		sel := ""
		if selected[j] {
			sel = "■"
		}
		recs[i] = recordtable.Record{
			colSel:     sel,
			colStatus:  twin.Icon(j.Status()),
			colPath:    j.Path,
			colDelta:   twin.JobDelta(j),
			colChanges: cell(j),
			colFile:    jobFileLabel(program, j),
		}
	}
	return recs
}

// driftCell renders a verified directory's verdict.
func driftCell(d *twin.Drift) string {
	s := "–"
	if n := len(d.Pending); n > 0 {
		s = fmt.Sprintf("%d pending", n)
	}
	if n := len(d.Conflicts); n > 0 {
		s += fmt.Sprintf(" · %d conflict", n)
		if n > 1 {
			s += "s"
		}
	}
	return s
}

// statusCell renders what a file job's mtime status implies; blank for a
// directory, whose mtime says nothing.
func statusCell(j *twin.Job) string {
	if j.Directory {
		return ""
	}
	switch j.Status() {
	case twin.StatusSourceNewer:
		return "1 pending"
	case twin.StatusTargetNewer:
		return "held back"
	case twin.StatusInSync:
		return "–"
	}
	return ""
}

// jobFileLabel is the sync-file basename, prefixed with the original
// program name when it differs from the merged one ("livesync [agent]").
func jobFileLabel(program *twin.Program, j *twin.Job) string {
	file := filepath.Base(j.SyncFile)
	if program != nil && j.Program != program.Name {
		return j.Program + " · " + file
	}
	return file
}

// filterPrograms keeps programs whose name contains term (case-insensitive).
func filterPrograms(programs []*twin.Program, term string) []*twin.Program {
	if term == "" {
		return programs
	}
	term = strings.ToLower(term)
	var out []*twin.Program
	for _, p := range programs {
		if strings.Contains(strings.ToLower(p.Name), term) {
			out = append(out, p)
		}
	}
	return out
}

// filterJobs keeps jobs whose path contains term (case-insensitive).
func filterJobs(jobs []*twin.Job, term string) []*twin.Job {
	if term == "" {
		return jobs
	}
	term = strings.ToLower(term)
	var out []*twin.Job
	for _, j := range jobs {
		if strings.Contains(strings.ToLower(j.Path), term) {
			out = append(out, j)
		}
	}
	return out
}

// programWidths sizes the stage-1 columns to their content.
func programWidths(programs []*twin.Program) map[string]int {
	name, files := 0, 0
	for _, p := range programs {
		name = max(name, lipgloss.Width(p.Name))
		files = max(files, lipgloss.Width(strings.Join(syncFileNames(p), " · ")))
	}
	return map[string]int{
		colStatus:  2,
		colProgram: clamp(name, 8, 40),
		colJobs:    5,
		colFiles:   clamp(files, 8, 40),
	}
}

// jobWidths sizes the stage-2 columns to their content.
func jobWidths(program *twin.Program, jobs []*twin.Job) map[string]int {
	path, file := 0, 0
	for _, j := range jobs {
		path = max(path, lipgloss.Width(j.Path))
		file = max(file, lipgloss.Width(jobFileLabel(program, j)))
	}
	return map[string]int{
		colSel:     1,
		colStatus:  2,
		colPath:    clamp(path, 8, 60),
		colDelta:   8,
		colChanges: 14,
		colFile:    clamp(file, 6, 40),
	}
}

func clamp(n, lo, hi int) int { return max(lo, min(n, hi)) }

// needsAttention counts programs whose status is neither in sync nor
// merely unverified or disabled.
func needsAttention(programs []*twin.Program) int {
	n := 0
	for _, p := range programs {
		switch p.Status() {
		case twin.StatusInSync, twin.StatusUnverified, twin.StatusDisabled:
		default:
			n++
		}
	}
	return n
}

// iconStatus maps a status glyph back to a status, for colouring rendered
// rows. Glyphs shared by two statuses ("!") share their colour anyway.
var iconStatus = func() map[rune]twin.Status {
	m := map[rune]twin.Status{}
	for status, icon := range twin.StatusIcons {
		m[[]rune(icon)[0]] = status
	}
	return m
}()

// colorizeRows gives every rendered table row the colour of its status,
// the way the fzf picker coloured whole lines. bubbles/table can't style
// rows and truncates cells with an ANSI-blind width, so the colour goes on
// afterwards: a data row carries no escape codes of its own, so any line
// that already has one (the header, the cursor row with its selection
// bar) is left alone, and the status glyph sits in the first cells.
func colorizeRows(view string) string {
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.ContainsRune(line, 0x1b) {
			continue
		}
		if status, ok := rowStatus(line); ok {
			lines[i] = twin.Colorize(status, line)
		}
	}
	return strings.Join(lines, "\n")
}

// rowStatus finds the status glyph within the leading cells of a row.
func rowStatus(line string) (twin.Status, bool) {
	for i, r := range line {
		if i > 12 {
			break
		}
		if s, ok := iconStatus[r]; ok {
			return s, true
		}
	}
	return "", false
}

// findProgram returns the merged program with that name, or nil.
func findProgram(programs []*twin.Program, name string) *twin.Program {
	for _, p := range programs {
		if p.Name == name {
			return p
		}
	}
	return nil
}
