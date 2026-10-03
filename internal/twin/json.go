package twin

import "time"

// The --json shapes. Field order and nullability follow the Ruby original,
// so consumers (check-ops reads status --json) see the same document.

// JobJSON is one job in --json output.
type JobJSON struct {
	Program           string     `json:"program"`
	Path              string     `json:"path"`
	Description       string     `json:"description"`
	Active            int        `json:"active"`
	Excludes          []string   `json:"excludes"`
	Owned             []string   `json:"owned"`
	Includes          []string   `json:"includes"`
	Label             string     `json:"label"`
	Source            string     `json:"source"`
	Target            string     `json:"target"`
	Cmd               string     `json:"cmd"`
	Delete            bool       `json:"delete"`
	Render            bool       `json:"render"`
	RenderOutdated    *bool      `json:"render_outdated"`
	TargetPathField   *string    `json:"target_path_field"`
	SyncFile          string     `json:"sync_file"`
	SourceExists      bool       `json:"source_exists"`
	TargetExists      bool       `json:"target_exists"`
	SourceMtime       *string    `json:"source_mtime"`
	TargetMtime       *string    `json:"target_mtime"`
	Conflict          bool       `json:"conflict"`
	TargetUnreachable bool       `json:"target_unreachable"`
	Directory         bool       `json:"directory"`
	ContentEqual      *bool      `json:"content_equal"`
	Drift             *DriftJSON `json:"drift"`
	Verify            bool       `json:"verify"`
	Status            Status     `json:"status"`
}

// DriftJSON is a Drift with conflicts reduced to their relative paths.
type DriftJSON struct {
	Pending   []string `json:"pending"`
	TimeOnly  []string `json:"time_only"`
	Conflicts []string `json:"conflicts"`
}

// ProgramJSON is one program in --json output.
type ProgramJSON struct {
	Name        string    `json:"name"`
	Status      Status    `json:"status"`
	SyncFile    string    `json:"sync_file"`
	Label       string    `json:"label"`
	Description string    `json:"description"`
	Jobs        []JobJSON `json:"jobs"`
}

// ToJSON converts a job for output.
func (j *Job) ToJSON() JobJSON {
	out := JobJSON{
		Program:           j.Program,
		Path:              j.Path,
		Description:       j.Description,
		Active:            j.Active,
		Excludes:          nonNil(j.Excludes),
		Owned:             nonNil(j.Owned),
		Includes:          nonNil(j.Includes),
		Label:             j.Label,
		Source:            j.Source,
		Target:            j.Target,
		Cmd:               j.Cmd,
		Delete:            j.Delete,
		Render:            j.Render,
		RenderOutdated:    j.RenderOutdated,
		SyncFile:          j.SyncFile,
		SourceExists:      j.SourceExists,
		TargetExists:      j.TargetExists,
		SourceMtime:       isoTime(j.SourceMtime),
		TargetMtime:       isoTime(j.TargetMtime),
		Conflict:          j.Conflict,
		TargetUnreachable: j.TargetUnreachable,
		Directory:         j.Directory,
		ContentEqual:      j.ContentEqual,
		Verify:            j.Verify(),
		Status:            j.Status(),
	}
	if j.TargetPathField != "" {
		tpf := j.TargetPathField
		out.TargetPathField = &tpf
	}
	if j.Drift != nil {
		rels := make([]string, 0, len(j.Drift.Conflicts))
		for _, e := range j.Drift.Conflicts {
			rels = append(rels, e.Rel)
		}
		out.Drift = &DriftJSON{
			Pending:   nonNil(j.Drift.Pending),
			TimeOnly:  nonNil(j.Drift.TimeOnly),
			Conflicts: rels,
		}
	}
	return out
}

// ToJSON converts a program for output.
func (p *Program) ToJSON() ProgramJSON {
	jobs := make([]JobJSON, 0, len(p.Jobs))
	for _, j := range p.Jobs {
		jobs = append(jobs, j.ToJSON())
	}
	return ProgramJSON{
		Name:        p.Name,
		Status:      p.Status(),
		SyncFile:    p.SyncFile(),
		Label:       p.Label(),
		Description: p.Description(),
		Jobs:        jobs,
	}
}

// ProgramsJSON converts a list of programs for output.
func ProgramsJSON(programs []*Program) []ProgramJSON {
	out := make([]ProgramJSON, 0, len(programs))
	for _, p := range programs {
		out = append(out, p.ToJSON())
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func isoTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}
