package twin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// StatusIcons are the one-glyph verdicts. Directory jobs without a drift
// result show ∘: their mtimes prove nothing, and the dry-runs that would
// prove something are too slow for a full listing — no claim instead of a
// wrong one.
var StatusIcons = map[Status]string{
	StatusSourceNewer:   "→",
	StatusTargetNewer:   "←",
	StatusInSync:        "✓",
	StatusUnverified:    "∘",
	StatusMissingTarget: "!",
	StatusMissingSource: "!",
	StatusBothMissing:   "✗",
	StatusUnreachable:   "?",
	StatusDisabled:      "–",
}

// StatusColors are ANSI sequences per status.
var StatusColors = map[Status]string{
	StatusSourceNewer:   "\x1b[33m", // yellow
	StatusTargetNewer:   "\x1b[36m", // cyan
	StatusInSync:        "\x1b[32m", // green
	StatusUnverified:    "\x1b[2m",  // dim
	StatusMissingTarget: "\x1b[31m", // red
	StatusMissingSource: "\x1b[31m", // red
	StatusBothMissing:   "\x1b[31m", // red
	StatusUnreachable:   "\x1b[31m", // red
	StatusDisabled:      "\x1b[2m",  // dim
}

const (
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// Colorize wraps text in the status colour.
func Colorize(status Status, text string) string {
	return StatusColors[status] + text + ansiReset
}

// Dim renders text dimmed.
func Dim(text string) string { return ansiDim + text + ansiReset }

// Bold renders text bold.
func Bold(text string) string { return ansiBold + text + ansiReset }

// Icon is the status glyph, "?" for anything unknown.
func Icon(status Status) string {
	if icon, ok := StatusIcons[status]; ok {
		return icon
	}
	return "?"
}

// bracketSuffix: a trailing bracket group ties a program to its base name
// by convention — "livesync [agent]" lists under "livesync". Only the
// suffix form counts; brackets elsewhere are just a name.
var bracketSuffix = regexp.MustCompile(`\s*\[[^\]]*\]$`)

// BaseName strips the bracket suffix.
func BaseName(name string) string {
	return strings.TrimSpace(bracketSuffix.ReplaceAllString(name, ""))
}

// MergePrograms yields one entry per base name (case-insensitive), even
// when it appears in several sync-files or as bracketed variants —
// "everything of X" is one pick. Display only: the data model, `sync -p`
// and JSON keep the per-file programs. A lone program passes through as the
// same object; the result is sorted by name.
func MergePrograms(programs []*Program) []*Program {
	var order []string
	groups := map[string][]*Program{}
	for _, p := range programs {
		k := strings.ToLower(BaseName(p.Name))
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], p)
	}
	merged := make([]*Program, 0, len(order))
	for _, k := range order {
		group := groups[k]
		if len(group) == 1 {
			merged = append(merged, group[0])
			continue
		}
		name := BaseName(group[0].Name)
		for _, p := range group {
			if !bracketSuffix.MatchString(p.Name) {
				name = p.Name
				break
			}
		}
		var jobs []*Job
		for _, p := range group {
			jobs = append(jobs, p.Jobs...)
		}
		merged = append(merged, &Program{Name: name, Jobs: jobs})
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return strings.ToLower(merged[i].Name) < strings.ToLower(merged[j].Name)
	})
	return merged
}

// FormatDelta says which side is newer and by how much: "src +5m",
// "tgt +2d", "in sync" within tolerance, "" without both mtimes.
func FormatDelta(sm, tm *time.Time) string {
	if sm == nil || tm == nil {
		return ""
	}
	seconds := int64(sm.Sub(*tm).Seconds())
	abs := seconds
	if abs < 0 {
		abs = -abs
	}
	if abs < 60 {
		return "in sync"
	}
	label := "tgt"
	if seconds > 0 {
		label = "src"
	}
	var unit string
	switch {
	case abs >= 86400:
		unit = fmt.Sprintf("%dd", abs/86400)
	case abs >= 3600:
		unit = fmt.Sprintf("%dh", abs/3600)
	default:
		unit = fmt.Sprintf("%dm", abs/60)
	}
	return label + " +" + unit
}

// JobDelta is FormatDelta for a job; a directory's mtime delta would
// mislead (it moves on every sync), so directory jobs show none.
func JobDelta(job *Job) string {
	if job.Directory {
		return ""
	}
	return FormatDelta(job.SourceMtime, job.TargetMtime)
}
