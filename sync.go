package twin

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// itemizeChange matches a line from `rsync --itemize-changes` describing a
// real change: an itemize code whose first column is the update type
// (< > c h *) and second the file type (f d L D S), e.g. ">f+++++++++",
// "cd+++++++++", "*deleting". No-op runs emit no such line; headers and the
// summary don't match. Deterministic — no scraping of prose.
var itemizeChange = regexp.MustCompile(`^[<>ch*][fdLDS]`)

// Transferred reports whether rsync listed at least one changed item.
func Transferred(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if itemizeChange.MatchString(line) {
			return true
		}
	}
	return false
}

// rsyncNoise is rsync's own chatter: the header, "created directory", and
// the transfer summary. Everything twin adds itself (cmd output,
// "skipped:", error text) survives, as does any itemize line describing an
// actual change.
var rsyncNoise = regexp.MustCompile(
	`^((sending|receiving) incremental file list$|created directory |sent [\d,]+ bytes|total size is [\d,]+)`)

// attrOnly is an itemize line whose first column is "." — permissions or a
// timestamp on an existing file, no data moved.
var attrOnly = regexp.MustCompile(`^\.[fdLDS]`)

// Summarize drops everything from an rsync run that does not describe a
// change.
func Summarize(output string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(output, "\n") {
		l := strings.TrimRight(line, " \t\r\n")
		if l == "" || rsyncNoise.MatchString(l) || attrOnly.MatchString(l) {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// Mounted reports whether path lives on a mounted volume other than the
// root filesystem: it walks up until a parent sits on a different device or
// "/" is reached.
func Mounted(p string) bool {
	if !exists(p) {
		return false
	}
	current, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	for current != "/" {
		parent := filepath.Dir(current)
		cd, ok1 := deviceOf(current)
		pd, ok2 := deviceOf(parent)
		if !ok1 || !ok2 {
			return false
		}
		if cd != pd {
			return true
		}
		current = parent
	}
	return false
}

func deviceOf(p string) (uint64, bool) {
	st, err := os.Stat(p)
	if err != nil {
		return 0, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(sys.Dev), true
}

// RenderJob renders a template job: read source, substitute {{vars}}, write
// if changed. Returns success, output and whether the target changed.
func RenderJob(cfg *Config, job *Job, dryRun bool) (bool, string, bool) {
	if job.IsRemote() {
		return false, "render: remote targets are not supported", false
	}
	src := job.SourcePath()
	tgt := job.TargetPath()

	st, err := os.Stat(src)
	if err != nil {
		return false, "source not found: " + src, false
	}
	if st.IsDir() {
		return false, "render: source must be a file, not a directory: " + src, false
	}

	vars, err := cfg.VarMap()
	if err != nil {
		return false, err.Error(), false
	}
	rendered, err := RenderFile(src, vars, job.Path)
	if err != nil {
		return false, err.Error(), false
	}

	if dryRun {
		return true, fmt.Sprintf("(dry-run) would render %s → %s", filepath.Base(src), tgt), false
	}

	if err := os.MkdirAll(filepath.Dir(tgt), 0o755); err != nil {
		return false, err.Error(), false
	}
	current, err := os.ReadFile(tgt)
	changed := err != nil || !bytes.Equal(current, rendered)

	var output string
	if changed {
		if err := os.WriteFile(tgt, rendered, 0o644); err != nil {
			return false, err.Error(), false
		}
		output = fmt.Sprintf("rendered %s → %s", filepath.Base(src), tgt)
	} else {
		output = filepath.Base(src) + ": content unchanged"
	}

	if job.Cmd != "" {
		if changed {
			ok, out := runHook(job.Cmd)
			output += out
			if !ok {
				return false, output, changed
			}
		} else {
			output += "\ncmd skipped (content unchanged)"
		}
	}
	return true, output, changed
}

// runHook runs a Cmd hook through sh and formats its output for the job
// report.
func runHook(command string) (bool, string) {
	out, code, err := RunCommand("sh", "-c", command)
	text := "\ncmd: " + command + "\n" + out
	if err != nil {
		return false, text + err.Error()
	}
	if code != 0 {
		return false, text + fmt.Sprintf("cmd failed (exit %d)", code)
	}
	return true, text
}

// RunJob syncs one job. Returns success, combined output, and whether rsync
// actually moved bytes (false on no-op or dry run).
func RunJob(cfg *Config, job *Job, dryRun, force bool) (bool, string, bool) {
	if job.Render {
		return RenderJob(cfg, job, dryRun)
	}
	src := job.SourcePath()
	tgt := job.TargetPath()

	if !exists(src) {
		return false, "source not found: " + src, false
	}

	if job.IsRemote() {
		host, rpath := SplitRemote(tgt)
		dir := path.Dir(rpath)
		if !dryRun && !MkdirP(host, dir, job.Sudo) {
			return false, fmt.Sprintf("ssh: could not create %s on %s", dir, host), false
		}
	} else if err := os.MkdirAll(filepath.Dir(tgt), 0o755); err != nil {
		return false, err.Error(), false
	}

	output, code, err := RunCommand(RsyncArgs(cfg, job, dryRun, force)...)
	if err != nil {
		return false, err.Error(), false
	}
	if code != 0 {
		return false, output, false
	}

	xfr := !dryRun && Transferred(output)

	// Conflict is content-verified by the scanner and only ever set on file
	// jobs — a directory's mtime says nothing, so directory jobs surface
	// target-side changes through the pre-run prompt instead.
	if job.Conflict && !xfr && !dryRun && !force {
		output += "\nskipped: target is newer, source not synced"
	}

	if job.Cmd != "" && !dryRun {
		if xfr {
			ok, out := runHook(job.Cmd)
			output += out
			if !ok {
				return false, output, xfr
			}
		} else {
			output += "\ncmd skipped (nothing transferred)"
		}
	}
	return true, output, xfr
}

// RsyncArgs is the full rsync argument vector for a job.
//
// force drops --update, so a file that is newer on the target is
// overwritten anyway. Only ever set after the user agreed to it, or via
// `twin sync --force`.
func RsyncArgs(cfg *Config, job *Job, dryRun, force bool) []string {
	src := job.SourcePath()
	tgt := job.TargetPath()

	args := []string{"rsync", "-av", "--itemize-changes"}
	if !force {
		args = append(args, "--update")
	}
	if job.Delete {
		args = append(args, "--delete")
		args = append(args, BackupArgs(job)...)
	}
	if job.Sudo {
		// sudo rsync on the far side writes where only admin's sudo may.
		// --chown pins ownership: -a would transplant the source UID onto
		// the target (UNKNOWN:root on a host without that UID — and a user
		// created with it later would own a script that runs as root).
		args = append(args, "--rsync-path=sudo rsync", "--chown=root:root")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	for _, ex := range cfg.GlobalExcludes {
		args = append(args, "--exclude="+ex)
	}
	for _, ex := range job.AllExcludes() {
		args = append(args, "--exclude="+ex)
	}
	// After the excludes: rsync's first match wins, so .DS_Store, Exclude
	// and Own still hold inside an included directory.
	args = append(args, IncludeArgs(job.Includes)...)
	if isDir(src) {
		return append(args, src+"/", tgt+"/")
	}
	return append(args, src, tgt)
}

// IncludeArgs turns a positive list into rsync filters, anchored at the
// transfer root: each entry matches as a file ("/flink") and as a directory
// with everything below it ("/Skripte/***"); a nested entry lets its parent
// directories through so rsync descends to it. A closing --exclude=* drops
// the rest — which, without --delete-excluded, also protects everything
// outside the list on the target from --delete. No entries, no filters.
func IncludeArgs(includes []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(arg string) {
		if !seen[arg] {
			seen[arg] = true
			out = append(out, arg)
		}
	}
	for _, inc := range includes {
		parts := strings.Split(inc, "/")
		for i := 1; i < len(parts); i++ {
			add("--include=/" + strings.Join(parts[:i], "/") + "/")
		}
		add("--include=/" + inc)
		add("--include=/" + inc + "/***")
	}
	if len(out) == 0 {
		return nil
	}
	return append(out, "--exclude=*")
}

// BackupArgs is the safety net for --delete: deleted and overwritten files
// land in a per-run backup dir on the target side
// (<target>/.twin-backup/<stamp>). rsync only creates the dir when it
// actually backs something up. The exclude keeps a backup dir inside the
// transfer root (Path: ".") from being deleted by the very sync it protects
// against.
func BackupArgs(job *Job) []string {
	root := job.Target
	if job.IsRemote() {
		_, root = SplitRemote(job.Target)
	}
	dir := joinPath(joinPath(root, ".twin-backup"), runStamp())
	return []string{"--backup", "--backup-dir=" + dir, "--exclude=.twin-backup/"}
}

var (
	stampOnce sync.Once
	stamp     string
)

// runStamp is one timestamp per twin process, so a multi-job run shares a
// backup dir.
func runStamp() string {
	stampOnce.Do(func() { stamp = time.Now().Format("2006-01-02_150405") })
	return stamp
}

// JobResult is the outcome of syncing one job.
type JobResult struct {
	Job         *Job
	OK          bool
	Output      string
	Transferred bool
}

// RunProgram syncs all active jobs of a program.
func RunProgram(cfg *Config, program *Program, dryRun, force bool) []JobResult {
	var results []JobResult
	for _, job := range program.ActiveJobs() {
		ok, out, xfr := RunJob(cfg, job, dryRun, force)
		results = append(results, JobResult{Job: job, OK: ok, Output: out, Transferred: xfr})
	}
	return results
}

// RunCommand runs a command and returns stdout+stderr and the exit code.
// The error is non-nil only when the command could not be started.
func RunCommand(args ...string) (string, int, error) {
	cmd := exec.Command(args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	output := stdout.String() + stderr.String()
	if runErr == nil {
		return output, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return output, exitErr.ExitCode(), nil
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return "", -1, fmt.Errorf("command not found: %s", args[0])
	}
	return output, -1, runErr
}
