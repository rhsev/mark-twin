package twin

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Status is the verdict on one job or program.
type Status string

const (
	StatusDisabled      Status = "disabled"
	StatusUnreachable   Status = "unreachable"
	StatusBothMissing   Status = "both_missing"
	StatusMissingSource Status = "missing_source"
	StatusMissingTarget Status = "missing_target"
	StatusTargetNewer   Status = "target_newer"
	StatusSourceNewer   Status = "source_newer"
	StatusUnverified    Status = "unverified"
	StatusInSync        Status = "in_sync"
)

// statusRank orders statuses worst first, for program aggregation.
// unverified ranks below the problems and above in_sync — left out, a
// forgotten entry would fall through to a false green.
var statusRank = []Status{
	StatusUnreachable, StatusBothMissing, StatusMissingSource, StatusMissingTarget,
	StatusTargetNewer, StatusSourceNewer, StatusUnverified, StatusDisabled, StatusInSync,
}

// Job is one YAML block from a sync-file, enriched with live filesystem
// state.
type Job struct {
	Program     string
	Path        string
	Description string
	Active      int
	Excludes    []string
	// Owned: paths inside the sync scope that the TARGET owns — machine-
	// specific config the source must never clobber. Same rsync effect as
	// Exclude, kept apart so status can name the intent.
	Owned  []string
	Label  string
	Source string
	Target string
	Cmd    string
	Delete bool
	Render bool
	// RenderOutdated is nil unless the job renders; then it says whether the
	// target differs from the rendered template.
	RenderOutdated  *bool
	TargetPathField string
	SyncFile        string

	SourceExists      bool
	TargetExists      bool
	SourceMtime       *time.Time
	TargetMtime       *time.Time
	Conflict          bool
	TargetUnreachable bool
	Directory         bool
	// ContentEqual is set only when mtimes drifted on a file job and the
	// bytes were compared.
	ContentEqual *bool
	Drift        *Drift
	// SkipVerify is `Verify: false` in the sync-file: no content
	// verification for this entry, ever — too big or too remote for md5 and
	// dry-run rounds. Status falls back to mtime for files and stays
	// unverified for directories.
	SkipVerify bool
}

// joinPath joins like Ruby's File.join: one slash at the seam, nothing
// else normalised — a "." path stays visible instead of vanishing.
func joinPath(a, b string) string {
	return strings.TrimSuffix(a, "/") + "/" + strings.TrimPrefix(b, "/")
}

// SourcePath is the full local source path.
func (j *Job) SourcePath() string { return joinPath(j.Source, j.Path) }

// TargetPath is the full target path, honouring Target-Path.
func (j *Job) TargetPath() string {
	p := j.Path
	if j.TargetPathField != "" {
		p = j.TargetPathField
	}
	return joinPath(j.Target, p)
}

// IsRemote reports whether the target is an ssh target.
func (j *Job) IsRemote() bool { return IsRemote(j.Target) }

// Verify is the positive form of SkipVerify.
func (j *Job) Verify() bool { return !j.SkipVerify }

// AllExcludes is everything rsync must not touch: Exclude (not part of the
// sync at all) plus Own (part of the scope, but the target owns it).
func (j *Job) AllExcludes() []string {
	out := append([]string{}, j.Excludes...)
	return append(out, j.Owned...)
}

// Status judges the job.
func (j *Job) Status() Status {
	switch {
	case j.Active != 1:
		return StatusDisabled
	case j.TargetUnreachable:
		return StatusUnreachable
	case !j.SourceExists && !j.TargetExists:
		return StatusBothMissing
	case !j.SourceExists:
		return StatusMissingSource
	case !j.TargetExists:
		return StatusMissingTarget
	}
	// Render jobs compare by content, not mtime — a rendered target's mtime
	// bears no relation to the template's.
	if j.Render {
		if j.RenderOutdated != nil && *j.RenderOutdated {
			return StatusSourceNewer
		}
		return StatusInSync
	}
	// Directory jobs carry no mtime verdict at all: a directory's mtime
	// records the last entry added or removed — after a sync that is the
	// sync itself, and even an *excluded* file moves it. `twin status`
	// fills in Drift by asking rsync; without it the honest answer is "not
	// checked", not a guess.
	if j.Directory {
		switch {
		case j.Drift == nil:
			return StatusUnverified
		case len(j.Drift.Conflicts) > 0:
			return StatusTargetNewer
		case len(j.Drift.Pending) > 0:
			return StatusSourceNewer
		}
		return StatusInSync
	}
	// Same bytes under a newer timestamp (a `cat >` copy) is not drift.
	if j.ContentEqual != nil && *j.ContentEqual {
		return StatusInSync
	}
	if j.Conflict {
		return StatusTargetNewer
	}
	if j.SourceMtime == nil || j.TargetMtime == nil {
		return StatusInSync
	}
	delta := j.SourceMtime.Sub(*j.TargetMtime)
	if delta.Abs() < mtimeTolerance {
		return StatusInSync
	}
	if delta > 0 {
		return StatusSourceNewer
	}
	return StatusTargetNewer
}

// mtimeTolerance keeps timestamp jitter from ever reading as drift.
const mtimeTolerance = 60 * time.Second

// Program is a logical group of jobs sharing a program name within one
// sync-file — the selection unit in the picker.
type Program struct {
	Name string
	Jobs []*Job
}

// SyncFile is the sync-file of the first job.
func (p *Program) SyncFile() string {
	if len(p.Jobs) == 0 {
		return ""
	}
	return p.Jobs[0].SyncFile
}

// Label is the label of the first job.
func (p *Program) Label() string {
	if len(p.Jobs) == 0 {
		return ""
	}
	return p.Jobs[0].Label
}

// Description joins the distinct job descriptions.
func (p *Program) Description() string {
	var parts []string
	seen := map[string]bool{}
	for _, j := range p.Jobs {
		if j.Description == "" || seen[j.Description] {
			continue
		}
		seen[j.Description] = true
		parts = append(parts, j.Description)
	}
	return strings.Join(parts, " / ")
}

// Status aggregates across jobs — worst first.
func (p *Program) Status() Status {
	have := map[Status]bool{}
	for _, j := range p.Jobs {
		have[j.Status()] = true
	}
	for _, s := range statusRank {
		if have[s] {
			return s
		}
	}
	return StatusInSync
}

// ActiveJobs are the jobs with Active: 1, in document order.
func (p *Program) ActiveJobs() []*Job {
	var out []*Job
	for _, j := range p.Jobs {
		if j.Active == 1 {
			out = append(out, j)
		}
	}
	return out
}

// NewestSourceMtime is the latest known source mtime, or nil.
func (p *Program) NewestSourceMtime() *time.Time {
	var newest *time.Time
	for _, j := range p.Jobs {
		if j.SourceMtime != nil && (newest == nil || j.SourceMtime.After(*newest)) {
			newest = j.SourceMtime
		}
	}
	return newest
}

// NewestTargetMtime is the latest known target mtime, or nil.
func (p *Program) NewestTargetMtime() *time.Time {
	var newest *time.Time
	for _, j := range p.Jobs {
		if j.TargetMtime != nil && (newest == nil || j.TargetMtime.After(*newest)) {
			newest = j.TargetMtime
		}
	}
	return newest
}

// ExpandPath expands a leading ~ and makes the path absolute, like Ruby's
// File.expand_path. Symlinks are left alone.
func ExpandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// baseName is filepath.Base without the "." it returns for an empty path.
func baseName(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}
