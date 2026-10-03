package twin

import (
	"bytes"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Remote (ssh) targets are written exactly as rsync understands them:
// "user@host:/path" or "host:/path". A target counts as remote when a colon
// appears before the first slash. Sources stay local — twin pushes.

var sshOpts = []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}

var remotePattern = regexp.MustCompile(`^[^/]+:`)

// IsRemote reports whether target is an ssh target.
func IsRemote(target string) bool {
	return remotePattern.MatchString(target)
}

// SplitRemote turns "user@host:/path" into ("user@host", "/path").
func SplitRemote(target string) (host, path string) {
	host, path, _ = strings.Cut(target, ":")
	return host, path
}

// Reachable is a non-interactive reachability probe (BatchMode: never asks
// for a password).
func Reachable(host string) bool {
	return exec.Command("ssh", sshArgs(host, "true")...).Run() == nil
}

func sshArgs(host string, rest ...string) []string {
	args := append([]string{}, sshOpts...)
	args = append(args, host)
	return append(args, rest...)
}

// The batch scripts below run through /bin/sh on the far side, wrapped in
// '...' so that *any* login shell passes them through literally (ssh hands
// the command to the login shell, which on a fish machine is not POSIX).
// The usual escape for an embedded single quote is parsed differently by
// fish, so the rule is simply not to need one — hence double quotes around
// the printf formats. A test guards the rule.

// StatScript stats many paths in one round-trip: paths over stdin, one per
// line; answers "path<TAB>epoch" or "path<TAB>-" for missing ones. Tries
// GNU stat, then BSD, then BusyBox `date -r` — covers Linux, macOS and
// OpenWrt. GNU must come first: BSD's `-f` takes the format, GNU's `-f`
// switches to filesystem mode and half-succeeds with a multi-line dump
// instead of failing, which poisons the chain for every tool behind it —
// found on the VPS 2026-09-27, where every mtime read as unknown. GNU's
// `-c` fails cleanly on BSD, so this order is safe both ways. An empty
// epoch means the whole chain failed: unknown, not 1970.
const StatScript = `while IFS= read -r p; do
  if [ -e "$p" ]; then
    m=$(stat -c %Y -- "$p" 2>/dev/null || stat -f %m -- "$p" 2>/dev/null || date -r "$p" +%s)
    printf "%s\t%s\n" "$p" "$m"
  else
    printf "%s\t-\n" "$p"
  fi
done
`

// MD5Script checksums many paths in one round-trip, same shape as
// StatScript; "-" for anything that is not a regular file. Tries BSD md5,
// then md5sum (GNU, BusyBox); the parameter expansion keeps only the hash.
// MD5 is drift detection here, not cryptography.
const MD5Script = `while IFS= read -r p; do
  if [ -f "$p" ]; then
    m=$(md5 -q "$p" 2>/dev/null || md5sum "$p" 2>/dev/null)
    m=${m%% *}
    printf "%s\t%s\n" "$p" "$m"
  else
    printf "%s\t-\n" "$p"
  fi
done
`

// PreflightScript reports which of the tools twin relies on exist on the
// far side, for `twin doctor`.
const PreflightScript = `for t in rsync stat date md5 md5sum; do
  if command -v "$t" >/dev/null 2>&1; then
    printf "%s\tok\n" "$t"
  else
    printf "%s\t-\n" "$t"
  fi
done
`

// RemoteStat is one answer from StatScript. A path absent from the map was
// not reported at all; Exists false is "-"; Exists true with a nil Mtime is
// there, but nobody could name its mtime.
type RemoteStat struct {
	Exists bool
	Mtime  *time.Time
}

// runScript ships one of the batch scripts to host with stdin lines. The
// error is non-nil when ssh itself failed (unreachable, not installed).
func runScript(host, script string, stdin []string) (string, error) {
	if strings.Contains(script, "'") {
		return "", errors.New("batch script must not contain single quotes")
	}
	cmd := exec.Command("ssh", sshArgs(host, "/bin/sh -c '"+script+"'")...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(strings.Join(stdin, "\n") + "\n")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// StatPaths stats paths on host in one ssh round-trip.
func StatPaths(host string, paths []string) (map[string]RemoteStat, error) {
	if len(paths) == 0 {
		return map[string]RemoteStat{}, nil
	}
	out, err := runScript(host, StatScript, paths)
	if err != nil {
		return nil, err
	}
	return ParseStats(out), nil
}

var digitsOnly = regexp.MustCompile(`^\d+$`)

// ParseStats reads StatScript output. Only a real epoch counts as a time.
func ParseStats(out string) map[string]RemoteStat {
	result := map[string]RemoteStat{}
	for _, line := range strings.Split(out, "\n") {
		path, m, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		switch {
		case m == "-":
			result[path] = RemoteStat{}
		case digitsOnly.MatchString(m):
			n, _ := strconv.ParseInt(m, 10, 64)
			t := time.Unix(n, 0)
			result[path] = RemoteStat{Exists: true, Mtime: &t}
		default:
			result[path] = RemoteStat{Exists: true}
		}
	}
	return result
}

var md5Hex = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// MD5Paths checksums paths on host in one ssh round-trip. The value is ""
// for anything that could not be read.
func MD5Paths(host string, paths []string) (map[string]string, error) {
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	out, err := runScript(host, MD5Script, paths)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		path, sum, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		if md5Hex.MatchString(sum) {
			result[path] = strings.ToLower(sum)
		} else {
			result[path] = ""
		}
	}
	return result, nil
}

// Preflight asks host which tools it has.
func Preflight(host string) (map[string]bool, error) {
	out, err := runScript(host, PreflightScript, nil)
	if err != nil {
		return nil, err
	}
	return ParsePreflight(out), nil
}

// ParsePreflight reads PreflightScript output.
func ParsePreflight(out string) map[string]bool {
	result := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		tool, state, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		result[tool] = state == "ok"
	}
	return result
}

// MkdirP creates a directory on the remote side; sudo mirrors the job's
// Sudo field, so a missing directory under a root-owned parent can be made
// by the same authority that will write into it.
func MkdirP(host, dir string, sudo bool) bool {
	args := []string{"mkdir", "-p", ShellEsc(dir)}
	if sudo {
		args = append([]string{"sudo", "-n"}, args...)
	}
	return exec.Command("ssh", sshArgs(host, args...)...).Run() == nil
}

// SudoOK reports whether host grants passwordless sudo — the thing a Sudo:
// job otherwise dies on only at write time, with rsync's least helpful error.
func SudoOK(host string) bool {
	return exec.Command("ssh", sshArgs(host, "sudo", "-n", "true")...).Run() == nil
}

// ShellEsc quotes one argument for the remote shell (ssh joins its
// arguments with spaces and hands the string to a shell).
func ShellEsc(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
