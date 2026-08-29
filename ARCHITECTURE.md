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
   Scanner          load_jobs → list[Job] → group(jobs) → list[Program]
      │
      ├──▶  CLI       list / status / sync / doctor
      │
      └──▶  Picker    fzf + apex preview, returns selected Program
                          │
                          ▼
                          │
                          ▼
                    Conflict   paired dry-runs → files --update would hold
                          │     back → content compare → ask once, or abort
                          ▼
                       Sync   rsync (or render) per Job, mount check, Cmd hook
```

## Package layout

```
lib/twin/
  version.rb
  remote.rb     ssh targets: detection, reachability, batched stat, mkdir
  template.rb   {{token}} substitution + render-file helper
  config.rb     ~/.config/twin/config.yaml loader; host table → var_map
  scanner.rb    Job, Program structs; grubber + template + stat → grouped Programs
  sync.rb       rsync / render execution, mount check, post-sync hook
  conflict.rb   target-side changes: detection via paired dry-runs, diffs
  journal.rb    append-only sync journal (~/.local/state/twin/log.jsonl)
  add.rb        `twin add` — interactive scaffolding of new sync entries
  picker.rb     fzf wrapper with apex preview
  cli.rb        subcommand dispatcher

bin/twin        entrypoint
test/test_pure.rb
```

## Data model

**Job** — one YAML block:

```
program, path, description, active, excludes, owned, label, source, target, cmd,
delete, render, render_outdated, target_path_field, sync_file,
source_exists, target_exists, source_mtime, target_mtime, conflict,
directory, content_equal, drift
```

`excludes` and `owned` both become `--exclude` (via `Job#all_excludes`); they
are kept apart so `status` can report intent.

mtime is never the verdict, only a pre-filter for file jobs — and even there
identical bytes under a drifted timestamp clear it (`content_equal`; remote
targets get one batched md5 round per host). `conflict` is therefore
content-verified when set. Directory jobs get no mtime judgment at all: a
directory's mtime moves on every sync and on every excluded file, so
`Job#status` reports `unverified` until `twin status` fills `drift` by asking
rsync (`Conflict.drift` — paired dry-runs, itemize classification, checksums
for timestamp-only candidates).

`Job#status` → one of `disabled / unreachable / both_missing / missing_source /
missing_target / target_newer / in_sync / source_newer / unverified`. Render
jobs derive status from content (`render_outdated`), not mtime.
`Job#target_path` joins `target` with `target_path_field || path`.

**Program** — group of Jobs sharing a `program` name:

```
name, jobs
```

`Program#status` aggregates jobs (worst state wins). Selection in the picker
operates on Programs, not individual Jobs.

**Job order is part of the contract.** Jobs of a Program run in the order their
YAML blocks appear in the sync-file. Sync-files rely on this — a `Cmd` that
restarts a service belongs in the last block, so it fires after every path is in
place. Reorder the jobs and a deploy restarts against half-written state,
without any error to show for it.

The order is guaranteed at both ends of the pipeline, not merely observed:

- grubber emits records in document order — first block, first record — for all
  three output formats, pinned by its own `TestBlockOrderFollowsDocument`
  across `Extract` and `StreamJSONL`.
- twin preserves it through `filter_map` and `group_by` (insertion order per
  key), pinned by `test_job_order_follows_document_order`.

Neither side may quietly sort.

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

`Config#var_map` flattens the host table for the (`host` → `target`) pair into
`{ "src.home" => …, "dst.home" => …, "dst.mount" => … }` — empty when no hosts
are configured (templating inert). See the **Templating** section.

Environment overrides: `TWIN_SYNC_DIR` (sync_dir), `TWIN_CONFIG` (config path),
`TWIN_HOST` (host).

## Sync-files

Markdown files. Frontmatter is the sync-relationship (source/target). YAML
blocks define paths. grubber merges frontmatter into each block so every
record is self-contained.

Multiple blocks may share the same `Program` value — these are treated as
one logical unit by twin.

## Templating

Substitution sits between grubber and `build_job`, so grubber never sees
`{{tokens}}` and stays untouched. `Scanner.load_jobs` calls
`Template.substitute_record` on each record's path-bearing fields (`Source`,
`Target`, `Path`, `Target-Path`, `Exclude`, `Cmd`) using `cfg.var_map`. This
*must* run before `build_job`, which immediately `stat`s the resolved paths.
Unknown `{{token}}` → hard error (never sync a half-rendered path).

Three namespaces, one fixed meaning each:

- `{{src.*}}` — the running host's own paths (read side, `Source:`).
- `{{dst.mount}}` — where the target is mounted here (write side, `Target:`).
- `{{dst.*}}` — the target's native paths, used in **rendered file content**.

The mount/native split is the crux: a file written to `/Volumes/ralf/…` but read
by the target machine must contain `/Users/ralf/…`. Path fields and file content
draw from different namespaces, so a token never means two things.

`{{` opens a YAML flow mapping, so templated values must be quoted in the
sync-file (`Source: "{{src.home}}"`) — as in Ansible. Without a `hosts` table
`var_map` is empty and substitution is a no-op.

See [docs/templating-design.md](docs/templating-design.md) for the full
rationale.

## Picker

Two stages:

1. **Stage 1 — program picker.** Multi-line NUL-separated entries (`--read0`).
   Each entry has a header (icon, program name, job count, sync-file) and
   indented body lines (one per job). No preview. Single-select. ESC exits.
2. **Stage 2 — path multi-picker.** Tab-delimited rows (`id\tdisplay`),
   `--with-nth=2` hides the `id`. Multi-select via Tab. Preview pane shows
   the *compact* view (frontmatter + intro + the heading section containing
   the YAML block for the highlighted path), rendered via
   `apex --plugins -t terminal256`. ESC returns to Stage 1.

Compact previews are pre-rendered to per-job tempfiles before fzf launches.
An `awk` lookup maps `{1}` (the id) → tempfile path.

## CLI

File argument resolution (`twin <arg>` and `--file=<arg>`):

- empty / missing       → scan `sync_dir`, no filter
- bare name (no `/`)    → scan `sync_dir`, filter by substring match
- path containing `/`   → expand, then:
  - directory           → scan that directory, no filter
  - file                → scan parent directory, filter by basename
  - neither             → raise "not found: …"

Unknown options (anything starting with `-` that isn't `--help`) print an
error pointing at `twin --help` and exit 1.

`twin sync` returns exit 1 when any job failed. `--quiet` suppresses output
for successful no-op jobs (conflicts, errors and real transfers still print);
`--skip-unavailable` skips unmounted/unreachable targets instead of aborting.
The combination is the unattended-run mode (launchd/cron).

Every non-dry-run job lands in the journal (`Journal.record`): one JSON line
in `~/.local/state/twin/log.jsonl` (`TWIN_STATE_DIR` overrides the directory)
with timestamp, program, path, target, `ok`, `changed`, and a truncated error
line on failure. `twin log [-n N] [--json]` reads it back. Journal write
failures warn once and never break a sync.

`twin add <path>` (`add.rb`) scaffolds a new entry: it matches the expanded
path against the token-substituted `Source:` frontmatter of every sync-file
(files with no or foreign roots drop out), computes `Path:` relative to the
chosen root, suggests excludes from a fixed list of generated/heavy dirs found
in the source (`SUGGEST_EXCLUDES`), rejects paths the file already has a
block for, and appends heading + prose stub + YAML block. With no covering
sync-file it can create one (frontmatter from prompts). The pure helpers
(`frontmatter`, `candidates`, `relative_path`, `suggest_excludes`,
`build_block`) are unit-tested; the prompt flow reads plain stdin, so it is
scriptable by piping answers.

`twin doctor` checks required tools (grubber, rsync, fzf), optional renderers
(apex, glow, bat), templating (host/target resolve, every `{{token}}` resolves),
and whether all configured sync targets are mounted. Exits 1 if any required
check fails.

## Remote targets

`Target: user@host:/path` (rsync notation; colon before the first slash) makes
a job remote — `Job#remote?`. Sources stay local, twin pushes.

- **Stat**: remote paths can't be `File.stat`ed, so `build_job` leaves them
  "missing" and `Scanner.fill_remote_stats` fills them in afterwards — one
  `ssh` round-trip per host for all its paths (`Remote.stat_paths`: paths over
  stdin, `path\tepoch` back; BSD `stat -f %m` with GNU `stat -c %Y` fallback).
  A failed ssh sets `target_unreachable` → status `:unreachable`; the scan
  itself never fails on a dead host.
- **Reachability** replaces the mount check (`Remote.reachable?`,
  `ssh -o BatchMode=yes … true` — key auth only, never prompts).
- **rsync** needs no changes: the target string is already in its remote
  syntax. Parent directories are created via `ssh host mkdir -p` first.
- **`Cmd`** still runs locally (`sh -c`); acting on the server means writing
  an `ssh host '…'` command in the sync-file.
- **`Render: true` + remote raises** at scan time — render reads/writes target
  content, which twin only does on local (mounted) paths.

## Sync

Before syncing:

1. **Mount check** — every unique local target root must be a mount point
   (`File.stat.dev != parent.dev`); remote targets must be ssh-reachable.
   Aborts otherwise.
2. **Conflict warning** — emits stderr listing jobs where the target is
   newer than the source. Continues anyway (`rsync --update` skips them).

Then per Job, **rsync path** (non-render):

```
rsync -av --itemize-changes --update [--delete] [--exclude=...]* src/ tgt/
```

`--delete` is added when the Job has `delete: true` (from `Delete: true`),
together with `--backup --backup-dir=<target>/.twin-backup/<run-stamp>` —
deleted and overwritten files are moved aside, not destroyed. The stamp is
per-process, so one run shares a backup dir; rsync only creates it when it
actually backs something up. `--exclude=.twin-backup/` protects the backup
dir from a `Path: "."` sync deleting it. For remote targets the backup dir
is the path part of the target (it lives on the receiving side).
`--itemize-changes` makes change detection deterministic: `Sync.transferred?`
matches itemize lines (`/\A[<>ch*][fdLDS]/` — `>f…`, `cd…`, `*deleting`),
covering files, directories and deletions, with no scraping of rsync's prose.

If `Cmd` is set, it runs via `sh -c` after rsync — but only when something was
actually transferred. No-op syncs (target up to date, or target newer and
skipped by `--update`) leave the hook silent. On failure the exit code is
included in the output and the job is marked failed.

When a job has a known conflict (`target_newer`) and nothing transferred, the
output notes `"skipped: target is newer, source not synced"`.

**Render path** (`render: true`) — `Sync.render_job`. Templates can't be
rsync'd byte-for-byte, so instead: read source, substitute `{{dst.*}}` in the
content, compare against the current target bytes, write only if they differ.
`changed` drives the same `Cmd` gate. Content-hash comparison (not mtime)
sidesteps the `--update` trap — a freshly rendered temp is always "newer".
Render is file-only; a directory source is an error. `twin status` mirrors this:
render-job status comes from the same content comparison (`render_outdated`),
so a stale target with a matching mtime is still flagged `source_newer`.

## External dependencies

| Tool      | Purpose                                    |
|-----------|--------------------------------------------|
| `grubber` | Markdown + YAML block extraction           |
| `rsync`   | File transfer                              |
| `fzf`     | Interactive selection                      |
| `apex`    | Markdown preview rendering in the terminal |

twin has no runtime gem dependencies — only stdlib (`yaml`, `json`,
`optparse`, `open3`, `fileutils`).
