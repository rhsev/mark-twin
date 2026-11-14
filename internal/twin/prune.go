package twin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// `twin prune` removes old backup runs. A Delete job moves what it deletes
// or overwrites to <target>/.twin-backup/<stamp>/ (see BackupArgs), and
// nothing else ever removes those directories.

const backupDirName = ".twin-backup"

const stampLayout = "2006-01-02_150405"

var stampName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{6}$`)

// BackupTargets are the targets that may hold backup runs: those of the
// jobs with Delete: true, each once, sorted.
func BackupTargets(jobs []*Job) []string {
	seen := map[string]bool{}
	var out []string
	for _, j := range jobs {
		if j.Delete && !seen[j.Target] {
			seen[j.Target] = true
			out = append(out, j.Target)
		}
	}
	sort.Strings(out)
	return out
}

// BackupDir is the directory that holds a target's backup runs.
func BackupDir(target string) string { return joinPath(target, backupDirName) }

// ListBackups names the backup runs under target, oldest first. Only names
// twin wrote itself count, so nothing else in the directory is ever pruned.
// A target without a backup directory has none.
func ListBackups(target string) ([]string, error) {
	var names []string
	if IsRemote(target) {
		host, dir := SplitRemote(BackupDir(target))
		cmd := exec.Command("ssh", sshArgs(host, "ls", "-1", ShellEsc(dir))...)
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			if strings.Contains(stderr.String(), "No such file") {
				return nil, nil
			}
			return nil, fmt.Errorf("ssh %s: %s", host, lastLine(stderr.String()))
		}
		names = strings.Split(out.String(), "\n")
	} else {
		entries, err := os.ReadDir(BackupDir(target))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	var runs []string
	for _, n := range names {
		if n = strings.TrimSpace(n); stampName.MatchString(n) {
			runs = append(runs, n)
		}
	}
	sort.Strings(runs)
	return runs, nil
}

// OldBackups are the runs stamped before cutoff. A name that does not parse
// as a stamp is kept.
func OldBackups(names []string, cutoff time.Time) []string {
	var old []string
	for _, n := range names {
		t, err := time.ParseInLocation(stampLayout, n, time.Local)
		if err == nil && t.Before(cutoff) {
			old = append(old, n)
		}
	}
	return old
}

// RemoveBackups deletes the named runs under target.
func RemoveBackups(target string, names []string) error {
	for _, n := range names {
		if !stampName.MatchString(n) {
			return fmt.Errorf("not a backup run: %s", n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	if !IsRemote(target) {
		for _, n := range names {
			if err := os.RemoveAll(joinPath(BackupDir(target), n)); err != nil {
				return err
			}
		}
		return nil
	}
	host, dir := SplitRemote(BackupDir(target))
	args := []string{"rm", "-rf", "--"}
	for _, n := range names {
		args = append(args, ShellEsc(joinPath(dir, n)))
	}
	out, err := exec.Command("ssh", sshArgs(host, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh %s: %s", host, lastLine(string(out)))
	}
	return nil
}
