// Command twin syncs configuration files between machines from
// self-documenting Markdown sync-files.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	twin "github.com/rhsev/mark-twin"
)

const usage = `twin — sync configuration files between machines

USAGE:
  twin                       TUI (all sync-files)
  twin <name>                TUI — sync-file by name in sync_dir
  twin <path>                TUI — file or directory (absolute or relative)
  twin list   [--all] [--label X] [--file X] [--json]
  twin status [--all] [--label X] [--file X] [--json]
  twin sync   [-p PATTERN] [--label X] [--file X] [--all] [--dry-run]
              [--quiet] [-v] [--skip-unavailable]
              [--force] [--skip-conflicts]
  twin add <path>            scaffold a new sync entry for a local path
  twin log    [-n N] [--json]  recent journal entries (default 20)
  twin doctor                check tools, renderers, targets, remote hosts
  twin --help                show this message
  twin --version             show the running version

FILE ARGUMENT:
  bare name (no /)  → matched by substring against sync-file names
  contains /        → resolved as path; file or directory both work

TARGET-SIDE CHANGES:
  Before syncing, twin looks for files the target changed more recently
  AND whose content differs, then asks once for the whole program.
  --force           overwrite them without asking (for automation)
  --skip-conflicts  leave them alone, sync everything else

CONFIG:
  ~/.config/twin/config.yaml
  TWIN_SYNC_DIR  overrides sync_dir
  TWIN_CONFIG    overrides config path
`

// errExit ends the run with exit 1 after the message has already been
// printed where it belongs.
var errExit = errors.New("exit 1")

var stdin = bufio.NewReader(os.Stdin)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, errExit) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(argv []string) error {
	first := ""
	if len(argv) > 0 {
		first = argv[0]
	}
	switch first {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "-V", "--version", "version":
		fmt.Printf("twin %s (build %s)\n", twin.Version, twin.Build)
		return nil
	}

	cfg, err := twin.LoadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	rest := argv
	if len(rest) > 0 {
		rest = rest[1:]
	}
	switch {
	case first == "":
		return pickAndSync(cfg, "")
	case first == "list":
		return cmdList(cfg, rest)
	case first == "status":
		return cmdStatus(cfg, rest)
	case first == "sync":
		return cmdSync(cfg, rest)
	case first == "add":
		return cmdAdd(cfg, rest)
	case first == "log":
		return cmdLog(rest)
	case first == "doctor":
		return cmdDoctor(cfg)
	case strings.HasPrefix(first, "-"):
		fmt.Fprintf(os.Stderr, "unknown option: %s\n", first)
		fmt.Fprintln(os.Stderr, "Run 'twin --help' for usage.")
		return errExit
	}
	return pickAndSync(cfg, first)
}

// newFlagSet is a quiet flag set: errors come back as values, never as
// printed usage.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

type filterOpts struct {
	showAll bool
	label   string
	file    string
	json    bool
}

func parseFilterOpts(args []string) (filterOpts, error) {
	var o filterOpts
	fs := newFlagSet("filter")
	fs.BoolVar(&o.showAll, "all", false, "")
	fs.StringVar(&o.label, "label", "", "")
	fs.StringVar(&o.file, "file", "", "")
	fs.BoolVar(&o.json, "json", false, "")
	return o, fs.Parse(args)
}

type syncOpts struct {
	filterOpts
	pattern         string
	dryRun          bool
	quiet           bool
	skipUnavailable bool
	force           bool
	skipConflicts   bool
	verbose         bool
}

func parseSyncOpts(args []string) (syncOpts, error) {
	var o syncOpts
	fs := newFlagSet("sync")
	fs.BoolVar(&o.showAll, "all", false, "")
	fs.StringVar(&o.label, "label", "", "")
	fs.StringVar(&o.file, "file", "", "")
	fs.BoolVar(&o.verbose, "v", false, "")
	fs.BoolVar(&o.verbose, "verbose", false, "")
	fs.BoolVar(&o.force, "force", false, "")
	fs.BoolVar(&o.skipConflicts, "skip-conflicts", false, "")
	fs.StringVar(&o.pattern, "p", "", "")
	fs.StringVar(&o.pattern, "pattern", "", "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.quiet, "q", false, "")
	fs.BoolVar(&o.quiet, "quiet", false, "")
	fs.BoolVar(&o.skipUnavailable, "skip-unavailable", false, "")
	return o, fs.Parse(args)
}

// isTerminal reports whether f is a character device — close enough to
// isatty for deciding whether to colour and whether to ask.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func printJSON(v any) error {
	data, err := twin.MarshalJSON(v, "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// readLine reads one answer from stdin; ok is false when stdin is closed.
func readLine() (string, bool) {
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}
