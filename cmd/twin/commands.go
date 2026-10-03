package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	twin "github.com/rhsev/mark-twin"
)

const stampLayout = "2006-01-02 15:04:05"

// ── list / status ────────────────────────────────────────────────────────────

func cmdList(cfg *twin.Config, args []string) error {
	opts, err := parseFilterOpts(args)
	if err != nil {
		return err
	}
	programs, err := twin.LoadPrograms(cfg, opts.file, opts.label, opts.showAll)
	if err != nil {
		return err
	}
	if opts.json {
		return printJSON(twin.ProgramsJSON(programs))
	}
	for _, p := range programs {
		mark := "✓"
		if p.Status() == twin.StatusDisabled {
			mark = "–"
		}
		fmt.Printf("%s  %s  — %s\n", mark, p.Name, p.Description())
	}
	return nil
}

func cmdStatus(cfg *twin.Config, args []string) error {
	opts, err := parseFilterOpts(args)
	if err != nil {
		return err
	}
	programs, err := twin.LoadPrograms(cfg, opts.file, opts.label, opts.showAll)
	if err != nil {
		return err
	}
	if err := verifyDrift(cfg, programs); err != nil {
		return err
	}

	if opts.json {
		return printJSON(twin.ProgramsJSON(programs))
	}

	tty := isTerminal(os.Stdout)
	for _, p := range programs {
		icon := twin.Icon(p.Status())
		name := p.Name
		if tty {
			icon = twin.Colorize(p.Status(), icon)
			name = twin.Bold(name)
		}
		fmt.Printf("%s  %s\n", icon, name)
		for _, j := range p.Jobs {
			conflict := ""
			if j.Conflict {
				conflict = "  !"
				if tty {
					conflict = "  " + twin.Colorize(twin.StatusTargetNewer, "!")
				}
			}
			fmt.Printf("    %s%s\n", j.Path, conflict)
			if j.Directory {
				// Directory mtimes prove nothing — the drift verdict replaces them.
				note := "(not checked — target unavailable)"
				switch {
				case j.Drift != nil:
					note = driftSummary(j.Drift)
				case !j.Verify():
					note = "(not checked — Verify: false)"
				}
				fmt.Printf("      %s\n", note)
			} else {
				src := "(not found)"
				if j.SourceExists && j.SourceMtime != nil {
					src = j.SourceMtime.Format(stampLayout)
				}
				tgt := "(not found)"
				if j.TargetExists && j.TargetMtime != nil {
					tgt = j.TargetMtime.Format(stampLayout)
				} else if j.TargetExists {
					// There, but the stat chain could not name a time.
					tgt = "(mtime unknown)"
				}
				if j.TargetUnreachable {
					tgt = "(unreachable)"
				}
				fmt.Printf("      src %s\n", src)
				fmt.Printf("      dst %s\n", tgt)
				if j.ContentEqual != nil && *j.ContentEqual {
					fmt.Println("      content identical, timestamps differ")
				}
			}
			if len(j.Includes) > 0 {
				fmt.Printf("      only %s\n", strings.Join(j.Includes, ", "))
			}
			// Named, not hidden: these belong to the target on purpose.
			if len(j.Owned) > 0 {
				fmt.Printf("      own %s\n", strings.Join(j.Owned, ", "))
			}
		}
	}
	return nil
}

// verifyDrift asks rsync what a sync of each directory job would actually do.
func verifyDrift(cfg *twin.Config, programs []*twin.Program) error {
	var jobs []*twin.Job
	for _, p := range programs {
		jobs = append(jobs, p.Jobs...)
	}
	return twin.FillDrift(cfg, jobs)
}

func driftSummary(d *twin.Drift) string {
	if d.InSync() {
		if len(d.TimeOnly) == 0 {
			return "in sync"
		}
		return fmt.Sprintf("in sync (%d timestamp-only)", len(d.TimeOnly))
	}
	var parts []string
	if len(d.Pending) > 0 {
		parts = append(parts, fmt.Sprintf("%d to sync: %s", len(d.Pending), listSome(d.Pending)))
	}
	if len(d.Conflicts) > 0 {
		rels := make([]string, len(d.Conflicts))
		for i, c := range d.Conflicts {
			rels[i] = c.Rel
		}
		parts = append(parts, fmt.Sprintf("%d changed on target: %s (twin sync will ask)", len(rels), listSome(rels)))
	}
	return strings.Join(parts, "; ")
}

func listSome(rels []string) string {
	const max = 3
	if len(rels) <= max {
		return strings.Join(rels, ", ")
	}
	return strings.Join(rels[:max], ", ") + ", …"
}

// ── sync ─────────────────────────────────────────────────────────────────────

func cmdSync(cfg *twin.Config, args []string) error {
	opts, err := parseSyncOpts(args)
	if err != nil {
		return err
	}
	programs, err := twin.LoadPrograms(cfg, opts.file, opts.label, opts.showAll)
	if err != nil {
		return err
	}
	if opts.pattern != "" {
		var kept []*twin.Program
		for _, p := range programs {
			if strings.Contains(strings.ToLower(p.Name), strings.ToLower(opts.pattern)) {
				kept = append(kept, p)
			}
		}
		programs = kept
	}
	if len(programs) == 0 {
		if !opts.quiet {
			fmt.Println("no matching programs")
		}
		return nil
	}
	allOK := true
	for _, p := range programs {
		ok, _, err := syncJobs(cfg, p, p.ActiveJobs(), opts)
		if err != nil {
			return err
		}
		allOK = allOK && ok
	}
	if !allOK {
		return errExit
	}
	return nil
}

// jobOutcome is what one job's sync did; the TUI keeps it on the row.
type jobOutcome struct {
	job         *twin.Job
	ok          bool
	transferred bool
	output      string
}

// syncJobs syncs the given jobs and reports whether every attempted job
// succeeded, plus the outcome of each job that ran. quiet prints only
// conflicts, errors and jobs that changed something; verbose prints rsync's full output instead of just the
// changes; skipUnavailable skips jobs whose target is unmounted or
// unreachable instead of aborting.
func syncJobs(cfg *twin.Config, program *twin.Program, jobs []*twin.Job, opts syncOpts) (bool, []jobOutcome, error) {
	var active []*twin.Job
	for _, j := range jobs {
		if j.Active == 1 {
			active = append(active, j)
		}
	}
	if len(active) == 0 {
		return true, nil, nil
	}

	// One availability check per unique target root: local targets must be
	// mounted volumes, remote ones reachable via ssh.
	availability := map[string]string{}
	var ready []*twin.Job
	var reasons []string
	seenReason := map[string]bool{}
	for _, j := range active {
		reason, checked := availability[j.Target]
		if !checked {
			reason = targetAvailability(j)
			availability[j.Target] = reason
		}
		if reason == "" {
			ready = append(ready, j)
		} else if !seenReason[reason] {
			seenReason[reason] = true
			reasons = append(reasons, reason)
		}
	}
	for _, reason := range reasons {
		if opts.skipUnavailable {
			if !opts.quiet {
				fmt.Println("skipped: " + reason)
			}
			continue
		}
		fmt.Fprintln(os.Stderr, "abort: "+reason)
		return false, nil, errExit
	}
	if len(ready) == 0 {
		return true, nil, nil
	}

	// Decide about target-side changes BEFORE the first byte moves: a partly
	// applied program is worse than none at all.
	force, abort, err := resolveConflicts(cfg, ready, opts)
	if err != nil {
		return false, nil, err
	}
	if abort {
		return false, nil, nil
	}

	headerPrinted := false
	allOK := true
	var outcomes []jobOutcome
	for _, job := range ready {
		success, output, transferred := twin.RunJob(cfg, job, opts.dryRun, force)
		outcomes = append(outcomes, jobOutcome{job: job, ok: success, transferred: transferred, output: output})
		if !opts.dryRun {
			twin.RecordJournal(job, success, transferred, output)
		}
		allOK = allOK && success
		if opts.quiet && success && !transferred {
			continue
		}
		if !headerPrinted {
			fmt.Println("→ " + program.Name)
			headerPrinted = true
		}
		fmt.Println("  • " + job.Path)
		shown := output
		if !opts.verbose && success {
			shown = twin.Summarize(output)
		}
		switch {
		case strings.TrimSpace(shown) != "":
			fmt.Print(indent(shown, "    "))
		case success && opts.dryRun:
			// A silent run reads as a missing result; say what it means.
			fmt.Println("    (dry-run: nothing would change)")
		case success:
			fmt.Println("    (nothing to transfer)")
		}
		if !success {
			fmt.Fprintln(os.Stderr, "  error syncing "+job.Path)
		}
	}
	return allOK, outcomes, nil
}

// indent prefixes every line; the result always ends in a newline.
func indent(text, prefix string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		b.WriteString(prefix)
		b.WriteString(line)
	}
	out := b.String()
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// resolveConflicts settles what happens to files the target changed more
// recently, before any job runs. force true means overwrite them; false
// leaves them (--update keeps them); abort means sync nothing at all.
//
// The mtime pre-filter on each job is coarse and fires often; only files
// whose content really differs reach the prompt. A prompt that cries wolf
// gets answered without reading it.
func resolveConflicts(cfg *twin.Config, jobs []*twin.Job, opts syncOpts) (force, abort bool, err error) {
	if opts.force {
		return true, false, nil
	}
	if opts.skipConflicts || opts.dryRun {
		return false, false, nil
	}

	// Verify: false jobs skip the detection round (too big to walk); rsync's
	// --update still keeps newer target files, they just aren't listed here.
	var conflicts []*twin.Entry
	for _, j := range jobs {
		if !j.Verify() {
			continue
		}
		found, err := twin.Detect(cfg, j)
		if err != nil {
			return false, false, err
		}
		conflicts = append(conflicts, found...)
	}
	if len(conflicts) == 0 {
		return false, false, nil
	}

	reportConflicts(conflicts)

	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "abort: target has changes of its own and there is no terminal to ask.")
		fmt.Fprintln(os.Stderr, "       re-run with --force to overwrite them, or --skip-conflicts to keep them.")
		return false, true, nil
	}

	for {
		fmt.Print("\noverwrite these on the target and sync? [y]es / [d]iff / [n]o (abort) ")
		answer, ok := readLine()
		if !ok {
			answer = "n"
		}
		switch strings.ToLower(answer) {
		case "y", "yes":
			return true, false, nil
		case "n", "no", "":
			fmt.Println("aborted — nothing was synced.")
			return false, true, nil
		case "d", "diff":
			showDiffs(conflicts)
		default:
			fmt.Println("please answer y, d or n.")
		}
	}
}

func reportConflicts(conflicts []*twin.Entry) {
	fmt.Fprintf(os.Stderr, "target has changed since the last sync — %d file(s) differ:\n", len(conflicts))
	for _, c := range conflicts {
		age := ""
		if delta, ok := c.AgeDelta(); ok {
			age = fmt.Sprintf(" (target %s newer)", formatAge(delta))
		}
		shown := c.Rel
		if c.Job.Path != c.Rel {
			shown = c.Job.Path + "/" + c.Rel
		}
		fmt.Fprintf(os.Stderr, "  ! %s%s\n", shown, age)
	}
	fmt.Fprintln(os.Stderr, "syncing would replace them with the source version.")
}

func showDiffs(conflicts []*twin.Entry) {
	for _, c := range conflicts {
		fmt.Println()
		fmt.Printf("── %s %s\n", c.Rel, strings.Repeat("─", max(0, 60-len([]rune(c.Rel)))))
		fmt.Println(twin.Diff(c))
	}
}

func formatAge(d time.Duration) string {
	s := int64(d.Abs().Seconds())
	switch {
	case s < 90:
		return fmt.Sprintf("%ds", s)
	case s < 5400:
		return fmt.Sprintf("%dm", s/60)
	case s < 172800:
		return fmt.Sprintf("%dh", s/3600)
	}
	return fmt.Sprintf("%dd", s/86400)
}

// targetAvailability is "" when the target can be synced right now, else a
// human-readable reason.
func targetAvailability(job *twin.Job) string {
	if job.IsRemote() {
		host, _ := twin.SplitRemote(job.Target)
		if twin.Reachable(host) {
			return ""
		}
		return host + " is not reachable via ssh"
	}
	if twin.Mounted(job.Target) {
		return ""
	}
	return job.Target + " is not a mounted volume"
}

// ── add ──────────────────────────────────────────────────────────────────────

func cmdAdd(cfg *twin.Config, args []string) error {
	result, err := twin.RunAdd(cfg, args, &twin.Prompter{In: stdin, Out: os.Stdout})
	if err != nil {
		return err
	}
	if result == nil || !result.DryRun {
		return nil
	}
	return cmdSync(cfg, []string{"-p", result.Program, "--file=" + filepath.Base(result.File), "--dry-run"})
}

// ── log ──────────────────────────────────────────────────────────────────────

func cmdLog(args []string) error {
	n := 20
	json := false
	fs := newFlagSet("log")
	fs.IntVar(&n, "n", 20, "")
	fs.BoolVar(&json, "json", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	entries := twin.JournalTail(n)
	if json {
		return printJSON(entries)
	}
	if len(entries) == 0 {
		fmt.Printf("journal is empty (%s)\n", twin.LogPath())
		return nil
	}

	tty := isTerminal(os.Stdout)
	for _, e := range entries {
		ts := e.TS
		if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
			ts = t.Format(stampLayout)
		}
		mark := "✗"
		if e.OK {
			mark = "✓"
		}
		if tty {
			status := twin.StatusBothMissing
			if e.OK {
				status = twin.StatusInSync
			}
			mark = twin.Colorize(status, mark)
		}
		note := "no-op"
		switch {
		case !e.OK:
			msg := ""
			if e.Error != nil {
				msg = *e.Error
			}
			note = "error: " + msg
		case e.Changed:
			note = "changed"
		}
		fmt.Printf("%s  %s  %s  %s  (%s)\n", ts, mark, e.Program, e.Path, note)
	}
	return nil
}

// ── doctor ───────────────────────────────────────────────────────────────────

func cmdDoctor(cfg *twin.Config) error {
	ok := true

	// First line, because "which version am I actually running" is the
	// question behind a surprising number of the others.
	fmt.Printf("twin %s (build %s)\n", twin.Version, twin.Build)
	fmt.Println()

	fmt.Println("Tools")
	for _, bin := range []string{"grubber", "rsync"} {
		if toolAvailable(bin) {
			fmt.Println("  ✓  " + bin)
		} else {
			fmt.Printf("  ✗  %s  (required — not found in PATH)\n", bin)
			ok = false
		}
	}

	fmt.Println("\nRenderers (preview)")
	foundRenderer := false
	for _, bin := range []string{"apex", "glow", "bat"} {
		if toolAvailable(bin) {
			fmt.Println("  ✓  " + bin)
			foundRenderer = true
		} else {
			fmt.Printf("  –  %s  (not installed)\n", bin)
		}
	}
	if !foundRenderer {
		fmt.Println("  ⚠   no renderer found — file preview will fall back to cat")
	}

	fmt.Println("\nTemplating")
	if len(cfg.Hosts) == 0 {
		fmt.Println("  –  no hosts configured")
	} else {
		for _, attr := range []struct{ name, value string }{{"host", cfg.Host}, {"target", cfg.Target}} {
			switch {
			case attr.value == "":
				fmt.Printf("  ✗  %s not set in config\n", attr.name)
				ok = false
			case cfg.Hosts[attr.value] != nil:
				fmt.Printf("  ✓  %s: %s\n", attr.name, attr.value)
			default:
				fmt.Printf("  ✗  %s %q not found in hosts\n", attr.name, attr.value)
				ok = false
			}
		}
	}

	fmt.Println("\nTargets")
	programs, err := twin.LoadPrograms(cfg, "", "", true)
	if err != nil {
		fmt.Printf("  ✗  %v\n", err)
		ok = false
	} else {
		if len(cfg.Hosts) > 0 {
			fmt.Println("  ✓  all template tokens resolved")
		}
		targets := uniqueTargets(programs)
		if len(targets) == 0 {
			fmt.Println("  (no programs loaded)")
		} else {
			var reachable []string
			seen := map[string]bool{}
			for _, tgt := range targets {
				switch {
				case twin.IsRemote(tgt):
					host, _ := twin.SplitRemote(tgt)
					if twin.Reachable(host) {
						fmt.Printf("  ✓  %s  (ssh)\n", tgt)
						if !seen[host] {
							seen[host] = true
							reachable = append(reachable, host)
						}
					} else {
						fmt.Printf("  ✗  %s  (ssh: %s not reachable)\n", tgt, host)
						ok = false
					}
				case twin.Mounted(tgt):
					fmt.Println("  ✓  " + tgt)
				default:
					fmt.Printf("  ✗  %s  (not mounted)\n", tgt)
					ok = false
				}
			}
			ok = doctorRemoteTools(reachable) && ok
			ok = doctorSudoHosts(programs, seen) && ok
		}
	}

	if ok {
		fmt.Println("\nAll checks passed.")
		return nil
	}
	fmt.Println("\nSome checks failed.")
	return errExit
}

func uniqueTargets(programs []*twin.Program) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range programs {
		for _, j := range p.Jobs {
			if !seen[j.Target] {
				seen[j.Target] = true
				out = append(out, j.Target)
			}
		}
	}
	sort.Strings(out)
	return out
}

func toolAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// doctorSudoHosts checks passwordless sudo wherever a Sudo: job points. A
// failure fails doctor: the sync itself would die only at write time, and
// rsync's error for a sudo password prompt is famously unhelpful.
func doctorSudoHosts(programs []*twin.Program, reachable map[string]bool) bool {
	seen := map[string]bool{}
	var hosts []string
	for _, p := range programs {
		for _, j := range p.Jobs {
			if !j.Sudo || j.Active != 1 || !j.IsRemote() {
				continue
			}
			host, _ := twin.SplitRemote(j.Target)
			if !seen[host] {
				seen[host] = true
				hosts = append(hosts, host)
			}
		}
	}
	if len(hosts) == 0 {
		return true
	}
	ok := true
	fmt.Println("\nSudo targets")
	for _, host := range hosts {
		switch {
		case !reachable[host]:
			fmt.Printf("  –  %s  (not reachable, sudo untested)\n", host)
		case twin.SudoOK(host):
			fmt.Printf("  ✓  %s  (sudo -n)\n", host)
		default:
			fmt.Printf("  ✗  %s  (sudo -n fails — Sudo: syncs die at write time)\n", host)
			ok = false
		}
	}
	return ok
}

// doctorRemoteTools probes each reachable ssh host once for what the far
// side must provide. A missing rsync fails doctor — the first sync would die
// mid-run with a raw protocol error. Missing stat/date or md5/md5sum only
// degrade (unknown mtimes, conservative conflicts), so they warn and say so.
func doctorRemoteTools(hosts []string) bool {
	if len(hosts) == 0 {
		return true
	}
	ok := true
	fmt.Println("\nRemote tools")
	for _, host := range hosts {
		tools, err := twin.Preflight(host)
		if err != nil {
			fmt.Printf("  ✗  %s  (probe failed)\n", host)
			ok = false
			continue
		}
		var problems []string
		if !tools["rsync"] {
			problems = append(problems, "rsync missing — syncs will fail (OpenWrt: opkg install rsync)")
			ok = false
		}
		if !tools["stat"] && !tools["date"] {
			problems = append(problems, "no stat or date — remote mtimes read as unknown")
		}
		if !tools["md5"] && !tools["md5sum"] {
			problems = append(problems, "no md5 or md5sum — timestamp-only files stay flagged as conflicts")
		}
		if len(problems) == 0 {
			have := []string{"rsync", pick(tools["stat"], "stat", "date"), pick(tools["md5"], "md5", "md5sum")}
			fmt.Printf("  ✓  %s  (%s)\n", host, strings.Join(have, ", "))
			continue
		}
		mark := "✗"
		if tools["rsync"] {
			mark = "⚠"
		}
		fmt.Printf("  %s  %s  %s\n", mark, host, strings.Join(problems, "; "))
	}
	return ok
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
