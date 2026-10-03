package twin

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// What would a sync actually change? mtimes cannot answer that — a
// directory's mtime records the last entry added or removed (after a sync:
// the sync itself), a file's mtime moves on a `cat >` copy that changed
// nothing, and rsync equalises directory mtimes on every run anyway. So we
// ask rsync, which has the answer already and knows its own matching rules
// better than any reimplementation would:
//
//  1. What would a *forced* run transfer? One dry-run without --update.
//     Empty means fully in sync — the common case and the cheap exit.
//  2. Of that, what does --update hold back? A second dry-run with
//     --update. Everything the forced run would move but the normal one
//     would not is exactly the set --update protects — files the target
//     owns more recently.
//  3. What differs only in timestamp? rsync's itemize flags say so
//     (">f..t" — time, same size). Those files are checksummed; identical
//     content is noise, not drift.

// Drift is the classified outcome of a forced dry-run for one job.
type Drift struct {
	// Pending are relative paths a sync would genuinely change.
	Pending []string
	// TimeOnly are same bytes, different timestamp; a sync merely aligns them.
	TimeOnly []string
	// Conflicts are target-side changes whose content differs.
	Conflicts []*Entry
}

// InSync is true when nothing would move and nothing is held back.
func (d *Drift) InSync() bool { return len(d.Pending) == 0 && len(d.Conflicts) == 0 }

// Entry is one file the target owns more recently than the source, with
// content that actually differs (or could not be checked — remote md5
// failed — which is treated as differing, because guessing the other way
// would overwrite work). TargetMtime is nil for remote targets.
type Entry struct {
	Job         *Job
	Rel         string
	SourcePath  string
	TargetPath  string
	SourceMtime *time.Time
	TargetMtime *time.Time
}

// AgeDelta is how much newer the target is; ok is false without both mtimes.
func (e *Entry) AgeDelta() (time.Duration, bool) {
	if e.SourceMtime == nil || e.TargetMtime == nil {
		return 0, false
	}
	return e.TargetMtime.Sub(*e.SourceMtime), true
}

// ChangeKind classifies one itemize line.
type ChangeKind string

const (
	KindDeleted ChangeKind = "deleted" // target-only file, removed by Delete: true
	KindNew     ChangeKind = "new"     // does not exist on the target yet
	KindContent ChangeKind = "content" // size differs, so the bytes certainly do
	KindTime    ChangeKind = "time"    // timestamp only; content equality still open
	KindAttrs   ChangeKind = "attrs"   // permissions/owner, nothing a sync-file cares about
)

// ItemizedEntry is one parsed change line.
type ItemizedEntry struct {
	Rel  string
	Kind ChangeKind
}

// entryLine matches an itemize change line: an update type and a file type
// (">f.st...... lib/foo.rb") or "*deleting". Attribute-only lines (leading
// ".") and the surrounding prose do not match.
var entryLine = regexp.MustCompile(`^(\*deleting|[<>ch][fdLDS]\S*)\s+(.+?)\s*$`)

// DetectDrift classifies one job's drift by asking rsync. nil for render
// jobs (they compare content already and never use --update) and when a
// side is missing (status reports that on its own). The error is rsync
// itself failing to run — a verdict cannot be given then, and pretending
// "in sync" would be the one wrong answer.
func DetectDrift(cfg *Config, job *Job) (*Drift, error) {
	if job.Render || !job.SourceExists || !job.TargetExists {
		return nil, nil
	}
	forced, err := Itemized(RsyncArgs(cfg, job, true, true))
	if err != nil {
		return nil, err
	}
	if len(forced) == 0 {
		return emptyDrift(), nil
	}
	normalEntries, err := Itemized(RsyncArgs(cfg, job, true, false))
	if err != nil {
		return nil, err
	}
	normal := map[string]bool{}
	for _, e := range normalEntries {
		normal[e.Rel] = true
	}
	var timeRels []string
	for _, e := range forced {
		if e.Kind == KindTime {
			timeRels = append(timeRels, e.Rel)
		}
	}
	equal := EqualityMap(job, timeRels)
	return Assemble(forced, normal, equal, func(rel string) *Entry {
		return ConflictEntry(job, rel)
	}), nil
}

func emptyDrift() *Drift {
	return &Drift{Pending: []string{}, TimeOnly: []string{}, Conflicts: []*Entry{}}
}

// Detect lists the target-side changes worth asking about, in the order
// rsync reports them. Empty when --update holds nothing back, or holds back
// only identical files.
func Detect(cfg *Config, job *Job) ([]*Entry, error) {
	d, err := DetectDrift(cfg, job)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return []*Entry{}, nil
	}
	return d.Conflicts, nil
}

// DriftCandidates are directory jobs whose drift is still unknown and
// answerable — both sides present, target reachable, not opted out via
// Verify: false. File jobs are content-checked by the scanner already.
func DriftCandidates(jobs []*Job) []*Job {
	var out []*Job
	for _, j := range jobs {
		if j.Drift == nil && j.Verify() && j.Directory && j.Active == 1 &&
			!j.TargetUnreachable && j.SourceExists && j.TargetExists {
			out = append(out, j)
		}
	}
	return out
}

// FillDrift fills Drift on every candidate by asking rsync — the dry-runs
// are subprocess I/O, so a few run in parallel. The first rsync failure is
// returned; jobs it reached stay unverified.
func FillDrift(cfg *Config, jobs []*Job) error {
	candidates := DriftCandidates(jobs)
	if len(candidates) == 0 {
		return nil
	}
	queue := make(chan *Job, len(candidates))
	for _, j := range candidates {
		queue <- j
	}
	close(queue)
	workers := min(4, len(candidates))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for range workers {
		wg.Go(func() {
			for j := range queue {
				d, err := DetectDrift(cfg, j)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				j.Drift = d
			}
		})
	}
	wg.Wait()
	return firstErr
}

// Assemble builds a Drift from parsed entries. A file the normal run would
// also transfer flows source→target as intended; one only the forced run
// would touch is being held back by --update — the target owns it more
// recently. Content decides whether that is a conflict or noise.
func Assemble(forced []ItemizedEntry, normal, equal map[string]bool, conflict func(rel string) *Entry) *Drift {
	d := emptyDrift()
	for _, e := range forced {
		switch e.Kind {
		case KindDeleted, KindNew:
			d.Pending = append(d.Pending, e.Rel)
		case KindContent:
			if normal[e.Rel] {
				d.Pending = append(d.Pending, e.Rel)
			} else {
				d.Conflicts = append(d.Conflicts, conflict(e.Rel))
			}
		case KindTime:
			switch {
			case equal[e.Rel]:
				d.TimeOnly = append(d.TimeOnly, e.Rel)
			case normal[e.Rel]:
				d.Pending = append(d.Pending, e.Rel)
			default:
				d.Conflicts = append(d.Conflicts, conflict(e.Rel))
			}
		}
	}
	return d
}

// Itemized runs rsync and parses its itemize output. A non-zero exit
// (unreadable target, protocol error) reads as "nothing to report"; rsync
// not starting at all is an error the caller must not swallow.
func Itemized(args []string) ([]ItemizedEntry, error) {
	output, code, err := RunCommand(args...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, nil
	}
	return ParseItemized(output), nil
}

// ParseItemized reads itemize lines into entries, dropping attribute-only
// lines and prose.
func ParseItemized(output string) []ItemizedEntry {
	var out []ItemizedEntry
	for _, line := range strings.Split(output, "\n") {
		m := entryLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		kind := Classify(m[1])
		if kind == KindAttrs {
			continue
		}
		out = append(out, ItemizedEntry{Rel: m[2], Kind: kind})
	}
	return out
}

// Classify maps itemize flags to a ChangeKind.
func Classify(flags string) ChangeKind {
	if flags == "*deleting" {
		return KindDeleted
	}
	body := ""
	if len(flags) > 2 {
		body = flags[2:]
	}
	switch {
	case strings.Contains(body, "+"):
		return KindNew
	case strings.Contains(body, "s"):
		return KindContent
	case strings.Contains(body, "t") || strings.Contains(body, "T"):
		return KindTime
	}
	return KindAttrs
}

// EqualityMap answers content equality for the time-only candidates. Local
// pairs are compared directly; remote targets get one batched md5 round per
// job, and an unanswered path counts as differing.
func EqualityMap(job *Job, rels []string) map[string]bool {
	result := map[string]bool{}
	if len(rels) == 0 {
		return result
	}
	if !job.IsRemote() {
		for _, r := range rels {
			result[r] = SameContent(resolve(job.SourcePath(), r), resolve(job.TargetPath(), r))
		}
		return result
	}
	host, _ := SplitRemote(job.Target)
	_, rbase := SplitRemote(job.TargetPath())
	remotePaths := make(map[string]string, len(rels))
	paths := make([]string, 0, len(rels))
	for _, r := range rels {
		p := rbase
		if job.Directory {
			p = joinPath(rbase, r)
		}
		remotePaths[r] = p
		paths = append(paths, p)
	}
	sums, err := MD5Paths(host, paths)
	if err != nil {
		sums = map[string]string{}
	}
	for _, r := range rels {
		local, ok := LocalMD5(resolve(job.SourcePath(), r))
		result[r] = ok && sums[remotePaths[r]] == local
	}
	return result
}

// ConflictEntry describes one held-back file.
func ConflictEntry(job *Job, rel string) *Entry {
	src := resolve(job.SourcePath(), rel)
	tgt := job.TargetPath()
	var tgtMtime *time.Time
	if !job.IsRemote() {
		tgt = resolve(tgt, rel)
		tgtMtime = mtimeOf(tgt)
	}
	return &Entry{
		Job: job, Rel: rel, SourcePath: src, TargetPath: tgt,
		SourceMtime: mtimeOf(src), TargetMtime: tgtMtime,
	}
}

// resolve joins rel onto a directory base; a job path may be a single
// file — rsync then itemizes its basename, and the job path is already the
// full path.
func resolve(base, rel string) string {
	if isDir(base) {
		return joinPath(base, rel)
	}
	return base
}

// SameContent compares two regular files by size, then by digest.
func SameContent(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil || !sa.Mode().IsRegular() {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil || !sb.Mode().IsRegular() {
		return false
	}
	if sa.Size() != sb.Size() {
		return false
	}
	da, err := digest(a)
	if err != nil {
		return false
	}
	db, err := digest(b)
	if err != nil {
		return false
	}
	return da == db
}

func digest(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LocalMD5 is the md5 hex digest of a file; ok is false for anything
// unreadable or not a file.
func LocalMD5(p string) (string, bool) {
	if !isFile(p) {
		return "", false
	}
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func mtimeOf(p string) *time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return nil
	}
	t := st.ModTime()
	return &t
}

// Diff is a unified diff for one entry, or a short note when it can't be
// produced.
func Diff(entry *Entry) string {
	if entry.Job.IsRemote() {
		return "  (remote target — no diff available)"
	}
	if !IsText(entry.SourcePath) || !IsText(entry.TargetPath) {
		return "  (binary or unreadable)"
	}
	out, _, err := RunCommand("diff", "-u",
		"--label", "target ("+entry.TargetPath+")", entry.TargetPath,
		"--label", "source ("+entry.SourcePath+")", entry.SourcePath)
	if err != nil {
		// The entry is listed because its content differs; without diff the
		// honest answer is "can't show", never "no difference".
		return "  (diff unavailable: " + err.Error() + ")"
	}
	if out == "" {
		return "  (no textual difference)"
	}
	return out
}

// IsText is a cheap heuristic: a NUL byte in the first 8 KiB means binary.
func IsText(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false
	}
	for _, b := range buf[:n] {
		if b == 0 {
			return false
		}
	}
	return true
}
