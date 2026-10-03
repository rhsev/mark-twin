package twin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LoadJobs runs grubber over scanPath (or the configured sync_dir) and
// builds one Job per YAML block.
func LoadJobs(cfg *Config, scanPath string) ([]*Job, error) {
	if _, err := exec.LookPath("grubber"); err != nil {
		return nil, errors.New("grubber not found in PATH")
	}
	dir := scanPath
	if dir == "" {
		dir = cfg.SyncDir
	}
	cmd := exec.Command("grubber", "extract", dir, "-b", "--format", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("grubber: %s", strings.TrimSpace(stderr.String()))
	}
	// UseNumber keeps numeric text as written ("1.0" stays "1.0"), the way
	// Ruby's JSON parser and to_s would have rendered it.
	var records []map[string]any
	dec := json.NewDecoder(&stdout)
	dec.UseNumber()
	if err := dec.Decode(&records); err != nil {
		return nil, fmt.Errorf("grubber returned invalid JSON: %v", err)
	}
	vars, err := cfg.VarMap()
	if err != nil {
		return nil, err
	}
	var jobs []*Job
	for _, r := range records {
		sub, err := SubstituteRecord(r, vars, recordContext(r))
		if err != nil {
			return nil, err
		}
		j, err := BuildJob(sub, vars)
		if err != nil {
			return nil, err
		}
		if j != nil {
			jobs = append(jobs, j)
		}
	}
	FillRemoteStats(jobs)
	FillLocalAvailability(jobs, Mounted)
	return jobs, nil
}

// FillLocalAvailability is the local counterpart of a failed ssh: a local
// target that is not a mounted volume marks its jobs unreachable. Without it
// an unmounted share reads as every file missing — missing_target, a finding
// — where the truth is that nothing could be looked at. The rule is the one
// sync applies before it writes (targetAvailability), so status and sync
// cannot disagree about what is there. mounted is a parameter so tests need
// no real volume.
func FillLocalAvailability(jobs []*Job, mounted func(string) bool) {
	verdict := map[string]bool{}
	for _, j := range jobs {
		if j.IsRemote() || j.Active != 1 {
			continue
		}
		ok, seen := verdict[j.Target]
		if !seen {
			ok = mounted(j.Target)
			verdict[j.Target] = ok
		}
		if ok {
			continue
		}
		j.TargetUnreachable = true
		j.TargetExists = false
		j.TargetMtime = nil
		j.Conflict = false
		j.ContentEqual = nil
		j.RenderOutdated = nil
	}
}

// FillRemoteStats stats remote targets in one ssh round-trip per host. A
// failed ssh marks that host's jobs unreachable instead of aborting the scan
// (local jobs stay usable).
func FillRemoteStats(jobs []*Job) {
	var hosts []string
	byHost := map[string][]*Job{}
	for _, j := range jobs {
		if !j.IsRemote() || j.Active != 1 {
			continue
		}
		host, _ := SplitRemote(j.Target)
		if _, seen := byHost[host]; !seen {
			hosts = append(hosts, host)
		}
		byHost[host] = append(byHost[host], j)
	}
	for _, host := range hosts {
		hostJobs := byHost[host]
		paths := make([]string, len(hostJobs))
		for i, j := range hostJobs {
			_, paths[i] = SplitRemote(j.TargetPath())
		}
		stats, err := StatPaths(host, paths)
		if err != nil {
			for _, j := range hostJobs {
				j.TargetUnreachable = true
			}
			continue
		}
		for i, j := range hostJobs {
			// A path absent from the answer counts as missing, not as unknown.
			st, present := stats[paths[i]]
			j.TargetExists = present && st.Exists
			j.TargetMtime = nil
			if present {
				j.TargetMtime = st.Mtime
			}
			j.Conflict = !j.Directory && j.SourceExists && j.TargetMtime != nil &&
				j.SourceMtime != nil && j.TargetMtime.Sub(*j.SourceMtime) >= mtimeTolerance
		}
		verifyRemoteFileContent(host, hostJobs)
	}
}

// verifyRemoteFileContent is the remote counterpart of the local content
// check in BuildJob: file jobs whose mtimes drifted get one batched md5
// round per host. Identical content clears the conflict — the timestamps
// merely disagree.
func verifyRemoteFileContent(host string, hostJobs []*Job) {
	var candidates []*Job
	for _, j := range hostJobs {
		if j.Verify() && !j.Directory && !j.Render && j.SourceExists && j.TargetExists &&
			j.SourceMtime != nil && j.TargetMtime != nil &&
			j.TargetMtime.Sub(*j.SourceMtime).Abs() >= mtimeTolerance {
			candidates = append(candidates, j)
		}
	}
	if len(candidates) == 0 {
		return
	}
	paths := make([]string, len(candidates))
	for i, j := range candidates {
		_, paths[i] = SplitRemote(j.TargetPath())
	}
	sums, err := MD5Paths(host, paths)
	if err != nil {
		return
	}
	for i, j := range candidates {
		remote, ok := sums[paths[i]]
		if !ok || remote == "" {
			continue
		}
		local, ok := LocalMD5(j.SourcePath())
		eq := ok && remote == local
		j.ContentEqual = &eq
		if eq {
			j.Conflict = false
		}
	}
}

// LoadPrograms loads and filters jobs, then groups them into programs.
// file is a sync-file argument (see ResolveFileArg); label filters on the
// frontmatter Label; showAll keeps inactive jobs.
func LoadPrograms(cfg *Config, file, label string, showAll bool) ([]*Program, error) {
	scanPath, nameFilter, err := ResolveFileArg(file)
	if err != nil {
		return nil, err
	}
	jobs, err := LoadJobs(cfg, scanPath)
	if err != nil {
		return nil, err
	}
	var kept []*Job
	for _, j := range jobs {
		if nameFilter != "" && !strings.Contains(j.SyncFile, nameFilter) {
			continue
		}
		if label != "" && j.Label != label {
			continue
		}
		if !showAll && j.Active != 1 {
			continue
		}
		kept = append(kept, j)
	}
	return Group(kept), nil
}

// ResolveFileArg turns a file argument into (scanPath, nameFilter):
//
//	""              → "", ""              scan sync_dir, no filter
//	path to a dir   → dir, ""             scan that dir, no filter
//	path to a file  → dirname, basename   scan parent dir, filter by filename
//	bare name       → "", name            scan sync_dir, filter by name
func ResolveFileArg(file string) (scanPath, nameFilter string, err error) {
	if file == "" {
		return "", "", nil
	}
	if strings.Contains(file, "/") || file == "." || file == ".." {
		expanded := ExpandPath(file)
		st, err := os.Stat(expanded)
		switch {
		case err == nil && st.IsDir():
			return expanded, "", nil
		case err == nil && st.Mode().IsRegular():
			return filepath.Dir(expanded), filepath.Base(expanded), nil
		}
		return "", "", fmt.Errorf("not found: %s", file)
	}
	return "", file, nil
}

// Group buckets jobs by (program, sync-file), keeping first-appearance
// order for programs and document order for jobs. Sync-files depend on the
// latter: a Cmd that restarts a service goes in the last block so it fires
// once every path is in place.
func Group(jobs []*Job) []*Program {
	type key struct{ program, file string }
	index := map[key]int{}
	var programs []*Program
	for _, j := range jobs {
		k := key{j.Program, j.SyncFile}
		if i, ok := index[k]; ok {
			programs[i].Jobs = append(programs[i].Jobs, j)
			continue
		}
		index[k] = len(programs)
		programs = append(programs, &Program{Name: j.Program, Jobs: []*Job{j}})
	}
	return programs
}

// recordContext names a record for error messages: "<Program> in <file>".
func recordContext(r map[string]any) string {
	return fmt.Sprintf("%s in %s", str(r["Program"]), baseName(str(r["_note_file"])))
}

// textFields are the block fields BuildJob reads as text.
var textFields = []string{
	"Program", "Path", "Description", "Label", "Source", "Target",
	"Target-Path", "Exclude", "Own", "Include", "Cmd", "_note_file",
}

// BuildJob turns one grubber record into a Job. Records without Path,
// Source or Target yield nil. Errors: Render on a remote target, a list or
// mapping where a single value belongs, and a stat failure that is not
// "missing" (a path component that is a file, a symlink loop, an I/O error)
// — a sync-file mistake must not read as a plausible status.
func BuildJob(r map[string]any, vars map[string]string) (*Job, error) {
	ctx := recordContext(r)
	fields := make(map[string]string, len(textFields))
	for _, f := range textFields {
		s, err := scalar(r[f], f, ctx)
		if err != nil {
			return nil, err
		}
		fields[f] = s
	}
	path := fields["Path"]
	source := fields["Source"]
	target := fields["Target"]
	targetPathField := fields["Target-Path"]
	if path == "" || source == "" || target == "" {
		return nil, nil
	}

	render := r["Render"] == true
	skipVerify := r["Verify"] == false
	excludes := SplitList(fields["Exclude"])
	owned := SplitList(fields["Own"])
	includes := IncludeList(fields["Include"])
	remote := IsRemote(target)

	if render && remote {
		return nil, fmt.Errorf("%s: Render is not supported for remote targets (%s)", fields["Program"], target)
	}

	// Sudo hands the sync root rights on the target, and Delete prunes.
	// Combined, a wrong Path lets rsync clean up a system directory as root
	// — so the combination is refused here, not warned about in a README.
	// Whoever needs both has to argue the case in code.
	sudo := r["Sudo"] == true
	if sudo && r["Delete"] == true {
		return nil, fmt.Errorf("%s: Sudo and Delete must not share a block", ctx)
	}
	// --rsync-path only changes the program the far side runs; on a local
	// target Sudo would silently do nothing, which reads as "worked".
	if sudo && !remote {
		return nil, fmt.Errorf("%s: Sudo needs a remote target (%s is local)", ctx, target)
	}

	tp := path
	if targetPathField != "" {
		tp = targetPathField
	}
	srcFull := joinPath(source, path)
	tgtFull := joinPath(target, tp)
	srcExists, srcMtime, err := statPath(srcFull)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", ctx, err)
	}
	// Remote targets are stat'ed in one batched ssh call after all jobs are
	// built (FillRemoteStats) — until then they read as missing.
	tgtExists, tgtMtime := false, (*time.Time)(nil)
	if !remote {
		tgtExists, tgtMtime, err = statPath(tgtFull)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", ctx, err)
		}
	}

	var renderOutdated *bool
	if render {
		renderOutdated = renderOutdatedFor(srcFull, tgtFull, vars, path)
	}

	// rsync mirrors directories, so a directory source means a directory
	// target — also for remote jobs, whose far side can't be inspected here.
	directory := srcExists && isDir(srcFull)

	// Include narrows a directory to named entries. On a file it would turn
	// the trailing --exclude=* against the file itself and sync nothing.
	if len(includes) > 0 && srcExists && !directory {
		return nil, fmt.Errorf("%s: Include needs a directory Path (%s is a file)", ctx, path)
	}

	// A file job whose mtimes drifted apart may still hold the same bytes
	// (a `cat >` copy before the first twin run). Check before judging; a
	// directory's own mtime is judged not at all (see Job.Status).
	var contentEqual *bool
	if !skipVerify && !render && !remote && !directory && srcExists && tgtExists &&
		srcMtime != nil && tgtMtime != nil && tgtMtime.Sub(*srcMtime).Abs() >= mtimeTolerance {
		eq := SameContent(srcFull, tgtFull)
		contentEqual = &eq
	}

	conflict := !render && !directory && !(contentEqual != nil && *contentEqual) &&
		srcExists && tgtExists && tgtMtime != nil && srcMtime != nil &&
		tgtMtime.Sub(*srcMtime) >= mtimeTolerance

	return &Job{
		Program:         fields["Program"],
		Path:            path,
		Description:     fields["Description"],
		Active:          toInt(r["Active"]),
		Excludes:        excludes,
		Owned:           owned,
		Includes:        includes,
		Label:           fields["Label"],
		Source:          source,
		Target:          target,
		Cmd:             fields["Cmd"],
		Delete:          r["Delete"] == true,
		Sudo:            sudo,
		Render:          render,
		RenderOutdated:  renderOutdated,
		TargetPathField: targetPathField,
		SyncFile:        fields["_note_file"],
		SourceExists:    srcExists,
		TargetExists:    tgtExists,
		SourceMtime:     srcMtime,
		TargetMtime:     tgtMtime,
		Conflict:        conflict,
		Directory:       directory,
		ContentEqual:    contentEqual,
		SkipVerify:      skipVerify,
	}, nil
}

// statPath reports whether path exists and its mtime. Only "not there" and
// "not allowed" count as missing; any other stat failure is returned, so a
// misconfiguration surfaces instead of masquerading as a missing file.
func statPath(path string) (bool, *time.Time, error) {
	st, err := os.Stat(path)
	switch {
	case err == nil:
		t := st.ModTime()
		return true, &t, nil
	case errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrPermission):
		return false, nil, nil
	}
	return false, nil, err
}

// SplitList turns a comma-separated block field into trimmed, non-empty
// entries.
func SplitList(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IncludeList reads the Include field: entries relative to Path, with
// leading and trailing slashes dropped — "Skripte/" and "/Skripte" both name
// the directory Skripte, and a bare "/" names nothing.
func IncludeList(value string) []string {
	out := []string{}
	for _, entry := range SplitList(value) {
		if p := strings.Trim(entry, "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// renderOutdatedFor answers, for a render job, whether the target is out of
// date with the rendered template. nil when the source is missing or a
// directory (status reports that on its own). True when the target is
// absent, differs, or the template can't be rendered (unresolved token) —
// i.e. needs attention.
func renderOutdatedFor(srcFull, tgtFull string, vars map[string]string, context string) *bool {
	if !isFile(srcFull) {
		return nil
	}
	outdated := true
	rendered, err := RenderFile(srcFull, vars, context)
	if err != nil {
		return &outdated
	}
	current, err := os.ReadFile(tgtFull)
	if err != nil {
		return &outdated
	}
	outdated = !bytes.Equal(current, rendered)
	return &outdated
}

// scalar renders a block field as text and refuses lists and mappings: a
// YAML list under Exclude would otherwise collapse into one bogus rsync
// pattern and exclude nothing.
func scalar(v any, field, context string) (string, error) {
	switch v.(type) {
	case []any:
		return "", fmt.Errorf("%s: %s must be a single value (comma-separated for several), got a list", context, field)
	case map[string]any:
		return "", fmt.Errorf("%s: %s must be a single value, got a mapping", context, field)
	}
	return str(v), nil
}

// str renders a JSON value the way Ruby's to_s would: nil is "", numbers
// keep their written form (json.Number), decoded floats lose their ".0".
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// toInt reads Active the way Ruby's to_i would: absent is 0, a numeric
// string is its leading integer.
func toInt(v any) int {
	switch x := v.(type) {
	case json.Number:
		return toInt(x.String())
	case float64:
		return int(x)
	case bool:
		if x {
			return 1
		}
	case string:
		s := strings.TrimSpace(x)
		end := 0
		for end < len(s) && (s[end] >= '0' && s[end] <= '9' || end == 0 && (s[end] == '-' || s[end] == '+')) {
			end++
		}
		n, _ := strconv.Atoi(s[:end])
		return n
	}
	return 0
}
