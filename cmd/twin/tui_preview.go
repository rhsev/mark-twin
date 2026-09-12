package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	bkexec "github.com/rhsev/matterbase/basekit/exec"
	"github.com/rhsev/matterbase/basekit/theme"

	twin "github.com/rhsev/mark-twin"
)

// renderer turns a job's compact sync-file excerpt into ANSI text wrapped
// to width. The TUI swaps in a stub for tests.
type renderer func(job *twin.Job, width int) string

// makeRenderer picks the first available markdown renderer: apex, glow,
// bat, else the plain excerpt — the same order the fzf picker used.
func makeRenderer(cfg *twin.Config) renderer {
	switch {
	case toolAvailable("apex"):
		return func(job *twin.Job, width int) string {
			return renderVia(job, func(path string) ([]byte, error) { return runApex(cfg, path, width) })
		}
	case toolAvailable("glow"):
		return func(job *twin.Job, width int) string {
			return renderVia(job, func(path string) ([]byte, error) {
				return bkexec.Run(context.Background(), 10*time.Second, withTermEnv(),
					"glow", "-s", "dark", "-w", strconv.Itoa(width), path)
			})
		}
	case toolAvailable("bat"):
		return func(job *twin.Job, width int) string {
			return renderVia(job, func(path string) ([]byte, error) {
				return bkexec.Run(context.Background(), 5*time.Second, withTermEnv(),
					"bat", "--color=always", "--language=markdown", "--style=plain", "--paging=never", path)
			})
		}
	}
	return func(job *twin.Job, _ int) string { return twin.ExtractCompact(job.SyncFile, job.Path) }
}

// renderVia writes the compact excerpt to a temp file, runs the tool on
// it, and falls back to the plain excerpt when the tool fails.
func renderVia(job *twin.Job, tool func(path string) ([]byte, error)) string {
	compact := twin.ExtractCompact(job.SyncFile, job.Path)
	if strings.TrimSpace(compact) == "" {
		return theme.Label.Render("(no block found for " + job.Path + ")")
	}
	f, err := os.CreateTemp("", "twin-preview-*.md")
	if err != nil {
		return compact
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(compact); err != nil {
		f.Close()
		return compact
	}
	f.Close()
	out, err := tool(f.Name())
	if err != nil || len(out) == 0 {
		return compact
	}
	return string(out)
}

// runApex renders path with apex, honouring the apex_* config fields;
// width comes from the pane unless the config pins one.
func runApex(cfg *twin.Config, path string, width int) ([]byte, error) {
	args := []string{path, "--plugins", "-t", "terminal256"}
	if v := anyString(cfg.ApexTheme); v != "" {
		args = append(args, "--theme", v)
	}
	if v := anyString(cfg.ApexCodeHighlight); v != "" {
		args = append(args, "--code-highlight", v)
	}
	if v := anyString(cfg.ApexCodeHighlightTheme); v != "" {
		args = append(args, "--code-highlight-theme", v)
	}
	w := anyString(cfg.ApexWidth)
	if w == "" && width > 0 {
		w = strconv.Itoa(width)
	}
	if w != "" {
		args = append(args, "--width", w)
	}
	return bkexec.Run(context.Background(), 10*time.Second, withTermEnv(), "apex", args...)
}

// anyString renders an untyped config value as apex would expect it.
func anyString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// withTermEnv makes sure a renderer sees a colour-capable terminal even
// when the TUI runs under a sparse environment.
func withTermEnv() []string {
	env := os.Environ()
	has := func(key string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				return true
			}
		}
		return false
	}
	if !has("TERM") {
		env = append(env, "TERM=xterm-256color")
	}
	if !has("COLORTERM") {
		env = append(env, "COLORTERM=truecolor")
	}
	return env
}

var (
	warnStyle = lipgloss.NewStyle().Foreground(theme.Warning)
	textStyle = lipgloss.NewStyle().Foreground(theme.Text)
)

// programOverview is the stage-1 preview: what a program contains, with
// each job in its status colour — the body the fzf picker folded into its
// multi-line entries.
func programOverview(p *twin.Program, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s\n", theme.Title.Render(p.Name),
		theme.Label.Render(fmt.Sprintf("  %d/%d active", len(p.ActiveJobs()), len(p.Jobs))))
	if d := p.Description(); d != "" {
		b.WriteString(textStyle.Render(d) + "\n")
	}
	b.WriteString(theme.Label.Render("files: "+strings.Join(syncFileNames(p), " · ")) + "\n\n")

	pathWidth := 0
	for _, j := range p.Jobs {
		pathWidth = max(pathWidth, lipgloss.Width(j.Path))
	}
	pathWidth = min(pathWidth, max(width-20, 10))
	multi := len(syncFileNames(p)) > 1
	var unverified []string
	for _, j := range p.Jobs {
		line := fmt.Sprintf("%s  %-*s  %s", twin.Icon(j.Status()), pathWidth, j.Path, twin.JobDelta(j))
		if multi {
			line += "  " + filepath.Base(j.SyncFile)
		}
		b.WriteString(twin.Colorize(j.Status(), line) + "\n")
		if !j.Verify() {
			unverified = append(unverified, j.Path)
		}
	}
	if len(unverified) > 0 {
		b.WriteString("\n" + warnStyle.Render("⚠ content checks off (Verify: false): "+strings.Join(unverified, ", ")) + "\n")
	}
	return b.String()
}
