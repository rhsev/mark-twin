package twin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRunJobSourceMissing(t *testing.T) {
	cfg := NewConfig()
	j := testJob(func(j *Job) {
		j.Path, j.Source, j.Target = "a.txt", "/nonexistent/src", "/nonexistent/tgt"
		j.SourceExists, j.TargetExists = false, false
	})
	ok, msg, _ := RunJob(cfg, j, false, false)
	if ok || !strings.Contains(msg, "source not found") {
		t.Errorf("%v %q", ok, msg)
	}
}

// Real `rsync -avi` output (itemize-changes).
const (
	rsyncNoop = "sending incremental file list\n\nsent 122 bytes  received 13 bytes  270.00 bytes/sec\ntotal size is 5  speedup is 0.04\n"
	rsyncXfer = "sending incremental file list\n>f+++++++++ a.txt\ncd+++++++++ sub/\n\nsent 228 bytes  received 66 bytes  588.00 bytes/sec\ntotal size is 5  speedup is 0.02\n"
	rsyncDel  = "sending incremental file list\n*deleting   sub/b.txt\n\nsent 103 bytes  received 33 bytes  272.00 bytes/sec\ntotal size is 3  speedup is 0.02\n"
)

func TestTransferred(t *testing.T) {
	if Transferred(rsyncNoop) {
		t.Error("noop")
	}
	if !Transferred(rsyncXfer) {
		t.Error("file and dir")
	}
	if !Transferred(rsyncDel) {
		t.Error("delete")
	}
}

const fullRun = `sending incremental file list
.d..tp..... ./
*deleting   obsolete.rb
>f+++++++++ added.rb
>f.s....... changed.rb
.f...p..... untouched.rb

sent 119 bytes  received 40 bytes  318.00 bytes/sec
total size is 14  speedup is 0.09 (DRY RUN)
`

func TestSummarize(t *testing.T) {
	out := Summarize(fullRun)
	want := "*deleting   obsolete.rb\n>f+++++++++ added.rb\n>f.s....... changed.rb\n"
	if out != want {
		t.Errorf("keeps only real changes:\n%q", out)
	}
	for _, noise := range []string{"incremental file list", "sent 119 bytes", "total size is", "untouched.rb", ".d..tp"} {
		if strings.Contains(out, noise) {
			t.Errorf("must drop %q", noise)
		}
	}
	out = Summarize("sending incremental file list\ncmd: curl -sf http://x/reload\nhook ran\n")
	if !strings.Contains(out, "cmd: curl") || !strings.Contains(out, "hook ran") {
		t.Error("keeps twin's own lines")
	}
	if !strings.Contains(Summarize("sending incremental file list\n\nskipped: target is newer, source not synced\n"), "skipped: target is newer") {
		t.Error("keeps the skipped note")
	}
	if got := Summarize("sending incremental file list\n\nsent 63 bytes  received 12 bytes  150.00 bytes/sec\ntotal size is 4,001  speedup is 53.35\n"); got != "" {
		t.Errorf("no-op summarizes to nothing: %q", got)
	}
	if strings.Contains(Summarize("created directory /tgt/new\n>f+++++++++ a.rb\n"), "created directory") {
		t.Error("created directory is noise")
	}
}

func plainConfig() *Config {
	cfg := NewConfig()
	cfg.SyncDir = "/sync"
	cfg.GlobalExcludes = []string{}
	return cfg
}

func has(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func find(args []string, prefix string) string {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return a
		}
	}
	return ""
}

func TestBackupArgs(t *testing.T) {
	cfg := plainConfig()
	job := func(mod func(*Job)) *Job {
		return testJob(func(j *Job) {
			j.Path, j.Delete = "www", true
			if mod != nil {
				mod(j)
			}
		})
	}
	args := RsyncArgs(cfg, job(nil), false, false)
	if !has(args, "--delete") || !has(args, "--backup") || !has(args, "--exclude=.twin-backup/") ||
		!strings.HasPrefix(find(args, "--backup-dir="), "--backup-dir=/tgt/.twin-backup/") {
		t.Errorf("delete adds backup args: %v", args)
	}
	args = RsyncArgs(cfg, job(func(j *Job) { j.Delete = false }), false, false)
	if has(args, "--backup") || find(args, "--backup-dir=") != "" {
		t.Errorf("no delete, no backup: %v", args)
	}
	args = RsyncArgs(cfg, job(func(j *Job) { j.Target = "ralf@server:/srv" }), false, false)
	if !strings.HasPrefix(find(args, "--backup-dir="), "--backup-dir=/srv/.twin-backup/") {
		t.Errorf("remote backup dir uses remote path: %v", args)
	}
	a := find(RsyncArgs(cfg, job(nil), false, false), "--backup-dir=")
	b := find(RsyncArgs(cfg, job(nil), false, false), "--backup-dir=")
	if a != b {
		t.Error("backup dir shared within run")
	}
}

func TestRsyncForceFlag(t *testing.T) {
	cfg := plainConfig()
	job := func(mod func(*Job)) *Job {
		return testJob(func(j *Job) {
			j.Path, j.Owned = "www", []string{}
			if mod != nil {
				mod(j)
			}
		})
	}
	if !has(RsyncArgs(cfg, job(nil), false, false), "--update") {
		t.Error("update is the default")
	}
	if has(RsyncArgs(cfg, job(nil), false, true), "--update") {
		t.Error("force drops update")
	}
	if !has(RsyncArgs(cfg, job(func(j *Job) { j.Owned = []string{"local.fish"} }), false, false), "--exclude=local.fish") {
		t.Error("owned paths become excludes")
	}
	args := RsyncArgs(cfg, job(func(j *Job) { j.Excludes, j.Owned = []string{"*.log"}, []string{"local.fish"} }), false, false)
	if !has(args, "--exclude=*.log") || !has(args, "--exclude=local.fish") {
		t.Errorf("excludes and owned both applied: %v", args)
	}
}

// Sudo adds exactly the two flags the VPS probe of 2026-09-27 established:
// sudo rsync on the far side, and --chown because -a would transplant the
// source UID (UNKNOWN:root on a host without a UID 501 — and a user created
// with it later would own a script that runs as root).
func TestRsyncSudoArgs(t *testing.T) {
	cfg := plainConfig()
	job := func(mod func(*Job)) *Job {
		return testJob(func(j *Job) {
			j.Path, j.Target, j.Sudo = "watchdog", "admin@vps:/usr/local/bin", true
			if mod != nil {
				mod(j)
			}
		})
	}
	args := RsyncArgs(cfg, job(nil), false, false)
	if !has(args, "--rsync-path=sudo rsync") || !has(args, "--chown=root:root") {
		t.Errorf("Sudo must add rsync-path and chown: %v", args)
	}
	args = RsyncArgs(cfg, job(func(j *Job) { j.Sudo = false }), false, false)
	if find(args, "--rsync-path=") != "" || find(args, "--chown=") != "" {
		t.Errorf("no Sudo, no privileged flags: %v", args)
	}
}

func renderCfg() *Config {
	cfg := NewConfig()
	cfg.SyncDir = "/tmp"
	cfg.Hosts = map[string]map[string]string{
		"mini": {"home": "/mini-home"},
		"book": {"home": "/book-home", "mount": "/mnt/book"},
	}
	cfg.Host, cfg.Target = "mini", "book"
	return cfg
}

func renderJob(src, tgt, cmd string) *Job {
	return &Job{
		Program: "p", Path: filepath.Base(src), Active: 1, Excludes: []string{},
		Source: filepath.Dir(src), Target: filepath.Dir(tgt), Cmd: cmd, Render: true,
		TargetPathField: filepath.Base(tgt), SourceExists: true,
	}
}

func TestRenderJob(t *testing.T) {
	cfg := renderCfg()

	d := t.TempDir()
	src := filepath.Join(d, "t.conf")
	os.WriteFile(src, []byte("home={{dst.home}}\n"), 0o644)
	tgt := filepath.Join(d, "out", "t.conf")
	ok, _, changed := RunJob(cfg, renderJob(src, tgt, ""), false, false)
	got, _ := os.ReadFile(tgt)
	if !ok || !changed || string(got) != "home=/book-home\n" {
		t.Errorf("renders tokens and writes file: %v %v %q", ok, changed, got)
	}

	d = t.TempDir()
	src = filepath.Join(d, "t.conf")
	os.WriteFile(src, []byte("home=/book-home\n"), 0o644)
	tgt = filepath.Join(d, "t.conf.out")
	os.WriteFile(tgt, []byte("home=/book-home\n"), 0o644)
	j := renderJob(src, tgt, "")
	j.TargetExists = true
	ok, out, changed := RunJob(cfg, j, false, false)
	if !ok || changed || !strings.Contains(out, "unchanged") {
		t.Errorf("skips write when identical: %v %v %q", ok, changed, out)
	}

	d = t.TempDir()
	j = &Job{Program: "p", Path: ".", Active: 1, Source: d, Target: d, Render: true, SourceExists: true}
	if ok, out, _ := RunJob(cfg, j, false, false); ok || !strings.Contains(out, "directory") {
		t.Errorf("errors on directory source: %v %q", ok, out)
	}

	d = t.TempDir()
	src = filepath.Join(d, "t.conf")
	os.WriteFile(src, []byte("x={{unknown.var}}\n"), 0o644)
	if ok, out, _ := RunJob(cfg, renderJob(src, filepath.Join(d, "out.conf"), ""), false, false); ok || !strings.Contains(out, "{{unknown.var}}") {
		t.Errorf("unknown token in content: %v %q", ok, out)
	}

	d = t.TempDir()
	src = filepath.Join(d, "t.conf")
	os.WriteFile(src, []byte("home={{dst.home}}\n"), 0o644)
	tgt = filepath.Join(d, "out.conf")
	ok, out, changed = RunJob(cfg, renderJob(src, tgt, ""), true, false)
	if !ok || changed || exists(tgt) || !strings.Contains(out, "dry-run") {
		t.Errorf("dry run does not write: %v %v %q", ok, changed, out)
	}

	d = t.TempDir()
	src = filepath.Join(d, "t.conf")
	os.WriteFile(src, []byte("x={{dst.home}}\n"), 0o644)
	tgt = filepath.Join(d, "out.conf")
	flag := filepath.Join(d, "cmd-ran")
	j = renderJob(src, tgt, "touch "+flag)
	RunJob(cfg, j, false, false)
	if !exists(flag) {
		t.Error("cmd should run on change")
	}
	os.Remove(flag)
	RunJob(cfg, j, false, false)
	if exists(flag) {
		t.Error("cmd should be skipped when unchanged")
	}
}
