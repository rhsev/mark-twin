# Architecture

## Overview

```
sync-files (.md)
      │
      ▼
   grubber          parse Markdown, extract YAML blocks, merge frontmatter
      │
      ▼
   Template         substitute {{tokens}} in record fields (no-op without hosts)
      │
      ▼
   Scanner          LoadJobs → []*Job → Group → []*Program
      │
      ├──▶  CLI       list / status / sync / add / log / doctor   (cmd/twin)
      │
      └──▶  TUI       two-stage table on basekit, preview pane    (cmd/twin)
                          │
                          ▼
                    Conflict   paired dry-runs → files --update would hold
                          │     back → content compare → ask once, or abort
                          ▼
                       Sync   rsync (or render) per Job, mount check, Cmd hook
```

## Package layout

Module `github.com/rhsev/mark-twin`. The engine is package `twin` in
`internal/twin` — internal because only the command uses it, so its exported
names are no public API under semver; the command lives in `cmd/twin`.

```
internal/twin/
  version.go    Version and the build stamp the Makefile injects
  remote.go     ssh targets: detection, reachability, batched stat/md5, mkdir
  template.go   {{token}} substitution + render-file helper
  config.go     ~/.config/twin/config.yaml loader; host table → VarMap
  job.go        Job, Program, Status; path joining
  scanner.go    grubber + template + stat → grouped Programs
  sync.go       rsync / render execution, mount check, post-sync hook
  conflict.go   target-side changes: detection via paired dry-runs, diffs
  journal.go    append-only sync journal (~/.local/state/twin/log.jsonl)
  add.go        `twin add`: scaffolding of new sync entries
  preview.go    compact excerpt of a sync-file for one block
  display.go    status icons and colours, MergePrograms, mtime deltas
  json.go       the --json shapes

cmd/twin/
  main.go         dispatcher, option parsing
  commands.go     list / status / sync / add / log / doctor
  tui.go          the TUI model (Bubble Tea)
  tui_rows.go     table rows, filters, column widths, row colouring
  tui_preview.go  renderers (apex / glow / bat / plain), program overview
  tui_exec.go     background commands and the sync hand-off
```

Tests sit beside the code (`*_test.go`); `go test ./...` runs them. The TUI
tests are headless: messages go into `Update`, commands are executed
inline, `View` is inspected as text.

## Data model

**Job** (job.go) is one YAML block:

```
Program, Path, Description, Active, Excludes, Owned, Label, Source, Target,
Cmd, Delete, Render, RenderOutdated, TargetPathField, SyncFile, SkipVerify,
SourceExists, TargetExists, SourceMtime, TargetMtime, Conflict,
TargetUnreachable, Directory, ContentEqual, Drift
```

`Excludes` and `Owned` both become `--exclude` (`Job.AllExcludes`); they are
kept apart so `status` can report intent. `Verify: false` is stored as
`SkipVerify`, so a zero-value Job verifies.

mtime is never the verdict, only a pre-filter for file jobs, and even there
identical bytes under a drifted timestamp clear it (`ContentEqual`; remote
targets get one batched md5 round per host). `Conflict` is therefore
content-verified when set. Directory jobs get no mtime judgment at all: a
directory's mtime moves on every sync and on every excluded file, so
`Job.Status` reports `unverified` until `Drift` is filled by asking rsync
(`DetectDrift`: paired dry-runs, itemize classification, checksums for
timestamp-only candidates). `Verify: false` opts a job out of every content
round; its status stays mtime-based (files) or `unverified` (directories).

`Job.Status` is one of `disabled / unreachable / both_missing /
missing_source / missing_target / target_newer / source_newer / unverified /
in_sync`. Render jobs derive status from content (`RenderOutdated`), not
mtime. `Job.TargetPath` joins `Target` with `TargetPathField` or `Path`.
Paths are joined like Ruby's `File.join` (one slash at the seam, nothing
normalised), so a `Path: "."` stays visible.

**Program** groups Jobs sharing a `Program` name within one sync-file:
`Name, Jobs`. `Program.Status` aggregates jobs (worst state wins). The TUI
and `sync` operate on Programs, not individual Jobs.

**Job order is part of the contract.** Jobs of a Program run in the order
their YAML blocks appear in the sync-file. A `Cmd` that restarts a service
belongs in the last block, so it fires after every path is in place.
grubber emits records in document order; `Group` keeps first-appearance
order for programs and document order for jobs, pinned by
`TestScannerGrouping`. Neither side may quietly sort.

## Configuration

`~/.config/twin/config.yaml`:

```yaml
sync_dir: /path/to/sync-files
global_excludes: [".DS_Store", ".git/"]
apex_theme: ralf
apex_width: 80

host: mini          # which host twin runs as
target: book        # default sync target
hosts:
  mini: { home: /Volumes/lightning/users/extern, git: /Volumes/lightning/Git }
  book: { home: /Users/ralf, git: /Users/ralf/git, mount: /Volumes/ralf }
```

`Config.VarMap` flattens the host table for the (`host` → `target`) pair into
`{"src.home": …, "dst.home": …, "dst.mount": …}`, empty when no hosts are
configured (templating inert). The `apex_*` fields are passed to the
renderer as written and never interpreted by twin.

Environment overrides: `TWIN_SYNC_DIR`, `TWIN_CONFIG`, `TWIN_HOST`.

## Sync-files

Markdown files. Frontmatter is the sync relationship (source/target). YAML
blocks define paths. grubber merges frontmatter into each block so every
record is self-contained. Multiple blocks may share the same `Program`.

grubber's JSON is decoded with `UseNumber`, so numeric text stays as written.
A list or mapping where a single value belongs is an error that names the
block; a block-level `Active` wins over the frontmatter's.

## Templating

Substitution sits between grubber and `BuildJob`, so grubber never sees
`{{tokens}}`. `LoadJobs` calls `SubstituteRecord` on each record's
path-bearing fields (`Source`, `Target`, `Path`, `Target-Path`, `Exclude`,
`Cmd`) using `Config.VarMap`. This must run before `BuildJob`, which
immediately stats the resolved paths. An unknown `{{token}}` is a hard error
(never sync a half-rendered path).

Three namespaces, one fixed meaning each:

- `{{src.*}}`: the running host's own paths (read side, `Source:`).
- `{{dst.mount}}`: where the target is mounted here (write side, `Target:`).
- `{{dst.*}}`: the target's native paths, used in **rendered file content**.

`{{` opens a YAML flow mapping, so templated values must be quoted in the
sync-file (`Source: "{{src.home}}"`). See
[docs/templating-design.md](docs/templating-design.md) for the rationale.

## TUI

`cmd/twin/tui*.go`, built on [basekit](https://github.com/rhsev/matterbase)
(`frame`, `input`, `recordtable`, `preview`, `theme`, `exec`). One screen:
filter on top, table in the middle, preview on the right, status and key
hints at the bottom.

1. **Stage 1: programs.** One `recordtable` row per merged program
   (`MergePrograms`: same name across sync-files becomes one entry,
   case-insensitive, and a trailing bracket group joins by convention).
   The merge is display-only; the data model, `status`, `sync -p` and JSON
   keep the per-file programs. The preview pane shows the program's paths
   in their status colour. `v` verifies every directory job in the
   background.
2. **Stage 2: paths of one program.** Columns: selection (`■`), status,
   path, mtime delta, `changes`, sync-file. Opening a program verifies its
   directory jobs (`DetectDrift`, at most four rsync dry-runs in flight);
   rows update as answers arrive. The preview pane shows the compact excerpt
   (`ExtractCompact`) rendered by apex, glow or bat, off the main loop and
   cached per job and pane width. `d` runs a dry run inside the TUI.

**Evidence survives reloads.** Verification verdicts, dry-run verdicts and
sync outcomes are keyed by the job's identity (sync-file, program, path),
not by the Job object a reload replaces. The `changes` column shows the most
recent of the three; a file's mtime status is the fallback. A synced job
drops its old verdict and is verified anew.

**Syncing stays in the TUI.** `enter` runs the same steps as the CLI's
`syncJobs`, which shares them (`partitionAvailable`, `detectConflicts`,
`runAndRecord`), as background commands: one plan (availability, conflict
detection) and then one command per job, so the status line can follow the
run and each row shows its outcome as it arrives. A `syncRun` in the model
holds the state; while it exists the keyboard is limited to moving around,
so no second sync or reload replaces the jobs under it. Conflicts hold the
run before the first byte: the preview shows `conflictReport`, `d` adds
`conflictDiffs`, `y` continues with force, `n` drops the run. At the end the
sync-files are reloaded and the same program reopens with the cursor where
it was. Until 1.2.1 the sync handed the terminal to the CLI path through
`tea.Exec` and waited for Enter.

Two Bubble Tea details worth knowing: `Init` runs on a copy of the model and
must not mutate it (the first load's generation is set in the constructor);
and `bubbles/table` truncates cells with an ANSI-blind width, so rows are
coloured after rendering (`colorizeRows`), by the status glyph in the first
cells.

## CLI

File argument resolution (`twin <arg>` and `--file=<arg>`):

- empty / missing       → scan `sync_dir`, no filter
- bare name (no `/`)    → scan `sync_dir`, filter by substring match
- path containing `/`   → expand, then:
  - directory           → scan that directory, no filter
  - file                → scan parent directory, filter by basename
  - neither             → error "not found: …"

Unknown options print an error pointing at `twin --help` and exit 1.
`--help` and `--version` work without a config.

`twin sync` returns exit 1 when any job failed. `--quiet` suppresses output
for successful no-op jobs; `--skip-unavailable` skips unmounted/unreachable
targets instead of aborting. The combination is the unattended-run mode.
A job that moved nothing says `(nothing to transfer)`.

Every non-dry-run job lands in the journal (`RecordJournal`): one JSON line
in `~/.local/state/twin/log.jsonl` (`TWIN_STATE_DIR` overrides the
directory). `twin log [-n N] [--json]` reads it back. Journal write failures
warn once and never break a sync.

`twin add <path>` matches the expanded path against the token-substituted
`Source:` of every sync-file, computes `Path:` relative to the chosen root,
suggests excludes from a fixed list of generated/heavy directories, rejects
paths the file already has a block for, and appends heading, placeholder
note and YAML block. The prompt flow reads plain stdin, so it is scriptable.

`twin doctor` checks required tools (grubber, rsync), optional renderers
(apex, glow, bat), templating, whether every target is mounted or reachable,
and what each reachable ssh host provides (`Preflight`). Exits 1 if a
required check fails.

## Remote targets

`Target: user@host:/path` (rsync notation; colon before the first slash)
makes a job remote (`IsRemote`). Sources stay local, twin pushes.

- **Stat**: `BuildJob` leaves remote targets "missing" and `FillRemoteStats`
  fills them in afterwards, one ssh round-trip per host, all hosts in
  parallel, and only for the jobs left after `--file`/`--label` filtering
  (`FillTargets` runs after the filter; `StatPaths`: paths
  over stdin, `path<TAB>epoch` back; BSD `stat -f`, GNU `stat -c`, then
  `date -r`). The answer is three-valued: `-` means missing, an empty or
  unparseable time means present with unknown mtime, and a path absent from
  the answer counts as missing. A failed ssh sets `TargetUnreachable`; the
  scan never fails on a dead host. `FillLocalAvailability` is the local
  counterpart: a local target that is not a mounted volume (`Mounted`, the
  rule `sync` checks before writing) sets `TargetUnreachable` too, so an
  unmounted share is not reported as every file `missing_target`.
- The batch scripts run through `/bin/sh -c '…'` explicitly, so the login
  shell on the far side does not matter; they contain no single quotes, and
  a test guards that.
- **Reachability** replaces the mount check (`ssh -o BatchMode=yes … true`).
- **rsync** needs no changes; parent directories are created via
  `ssh host mkdir -p` first.
- **`Cmd`** still runs locally (`sh -c`).
- **`Render: true` + remote** is an error at scan time.

## Sync

Before syncing: every unique local target root must be a mount point
(device differs from its parent's); remote targets must be reachable. Then
`Conflict.Detect` lists target-side changes whose content differs and asks
once for the whole program (`--force` overwrites, `--skip-conflicts` leaves
them; without a terminal and without either flag a real conflict aborts).

Per Job, **rsync path**:

```
rsync -av --itemize-changes --update [--delete] [--exclude=…]* src/ tgt/
```

`--delete` comes with `--backup --backup-dir=<target>/.twin-backup/<stamp>`;
one stamp per process. `--exclude=.twin-backup/` keeps a `Path: "."` sync
from deleting its own backups. `--itemize-changes` makes change detection
deterministic (`Transferred`: `>f…`, `cd…`, `*deleting`). `Cmd` runs via
`sh -c` only when something was transferred. When a job has a known
conflict and nothing transferred, the output notes
`skipped: target is newer, source not synced`.

**Render path** (`Render: true`, `RenderJob`): read source, substitute
`{{dst.*}}` in the content, compare against the current target bytes, write
only if they differ; `changed` drives the same `Cmd` gate.

Subprocesses that fail to *start* (rsync, diff, a hook) are errors, never a
clean verdict: `DetectDrift` returns the error and `twin status` aborts;
`Diff` says "diff unavailable" instead of "no textual difference". A stat
failure other than "not found" or "permission denied" surfaces with the
block named instead of reading as a missing file.

## External dependencies

| Tool      | Purpose                                            |
|-----------|----------------------------------------------------|
| `grubber` | Markdown + YAML block extraction (shell-out, JSON) |
| `rsync`   | File transfer                                      |
| `apex`, `glow`, `bat` | optional preview rendering, in that order |

Go modules: `gopkg.in/yaml.v3` (config, frontmatter), `charmbracelet/bubbletea`
and `lipgloss`, and `github.com/rhsev/matterbase` for basekit.
