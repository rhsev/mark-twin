# Architecture

## Overview

```
sync-files (.md)
      │
      ▼
   grubber          parse Markdown, extract YAML blocks, merge frontmatter
      │
      ▼
   Scanner          load_jobs → list[Job] → group(jobs) → list[Program]
      │
      ├──▶  CLI       list / status / sync
      │
      └──▶  Picker    fzf + apex preview, returns selected Program
                          │
                          ▼
                       Sync   rsync per Job, mount check, Cmd hook
```

## Package layout

```
lib/twin/
  version.rb
  config.rb     ~/.config/twin/config.yaml loader
  scanner.rb    Job, Program structs; grubber + stat → grouped Programs
  sync.rb       rsync execution, mount check, post-sync hook
  picker.rb     fzf wrapper with apex preview
  cli.rb        subcommand dispatcher

bin/twin        entrypoint
test/test_pure.rb
```

## Data model

**Job** — one YAML block:

```
program, path, description, active, excludes, label, source, target, cmd, sync_file,
source_exists, target_exists, source_mtime, target_mtime, conflict
```

`Job#status` → one of `disabled / both_missing / missing_source / missing_target /
target_newer / in_sync / source_newer`.

**Program** — group of Jobs sharing a `program` name:

```
name, jobs
```

`Program#status` aggregates jobs (worst state wins). Selection in the picker
operates on Programs, not individual Jobs.

## Configuration

`~/.config/twin/config.yaml`:

```yaml
sync_dir: /path/to/sync-files
global_excludes: [".DS_Store", ".git/"]
apex_theme: ralf
apex_width: 80
```

Environment overrides: `TWIN_SYNC_DIR` (sync_dir), `TWIN_CONFIG` (config path).

## Sync-files

Markdown files. Frontmatter is the sync-relationship (source/target). YAML
blocks define paths. grubber merges frontmatter into each block so every
record is self-contained.

Multiple blocks may share the same `Program` value — these are treated as
one logical unit by twin.

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

## Sync

Before syncing:

1. **Mount check** — every unique target root must be a mount point
   (`File.stat.dev != parent.dev`). Aborts if unmounted.
2. **Conflict warning** — emits stderr listing jobs where the target is
   newer than the source. Continues anyway (`rsync --update` skips them).

Then per Job:

```
rsync -av --update [--exclude=...]* src/ tgt/
```

If `Cmd` is set on the block and not in dry-run mode, the command is
executed via `sh -c` after a successful rsync.

## External dependencies

| Tool      | Purpose                                    |
|-----------|--------------------------------------------|
| `grubber` | Markdown + YAML block extraction           |
| `rsync`   | File transfer                              |
| `fzf`     | Interactive selection                      |
| `apex`    | Markdown preview rendering in the terminal |

twin has no runtime gem dependencies — only stdlib (`yaml`, `json`,
`optparse`, `open3`, `fileutils`).
