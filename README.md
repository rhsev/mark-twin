# twin

[![Gem Version](https://img.shields.io/gem/v/mark-twin.svg)](https://rubygems.org/gems/mark-twin)
[![Tests](https://github.com/rhsev/mark-twin/actions/workflows/test.yml/badge.svg)](https://github.com/rhsev/mark-twin/actions/workflows/test.yml)
[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Sync configuration between machines, from Markdown files that explain
themselves.

A sync-file is a normal Markdown document. The prose explains it to you — and
to whatever assistant you point at the file later. The fenced YAML blocks are
what twin acts on:

````markdown
---
Active: 1
Source: /Users/admin
Target: admin@macbook:/Users/admin
---

## Fish Shell

Shell config, including completions and abbreviations. `local.fish` stays
machine-specific — the laptop keeps its own.

```yaml
Program: Fish Shell
Path: .config/fish
Description: Fish Shell configuration
Own: conf.d/local.fish
```
````

`twin` then shows you what differs and syncs what you pick. Under the
hood it is `rsync`; the point is the file above, which still tells you in a
year *why* a path is synced, not just that it is.

That is the whole idea, and for most entries it stays exactly that small. The
rest of this README is long because twin does handle the awkward cases — files
the other machine owns, both sides changed since last time, paths that differ
per host — not because you need any of them on day one. Skip what you don't
need.

## Why Markdown

- **You can read it later.** The reason a path is synced lives next to the
  path, in prose, not in a comment you stopped writing after the third entry.
- **Machines can read it too.** The YAML blocks are extracted by
  [grubber](https://github.com/rhsev/grubber), so the same file can feed other
  tools, not just twin.
- **It stays editable by hand.** No generated state, no database. Add a block
  in your editor and twin picks it up.
- **And yes, ask your AI to write them.** Markdown with YAML blocks is exactly
  what language models are good at. Show one an existing sync-file, describe
  the next entry, and what comes back is valid for grubber and still readable
  by you — which is the whole bargain of this format.

## Screenshots

Stage 1 — program picker. One row per program, color-coded status, indented
paths underneath:

![Stage 1 — program picker](https://raw.githubusercontent.com/rhsev/mark-twin/main/docs/stage_1.png)

Stage 2 — multi-select over the paths of one program. The right pane shows a
compact preview of the relevant sync-file section, rendered by apex:

![Stage 2 — Fish Shell paths with apex preview](https://raw.githubusercontent.com/rhsev/mark-twin/main/docs/stage_2_fish.png)

## Install

```bash
gem install mark-twin
brew install fzf
```

Plus [grubber](https://github.com/rhsev/grubber/releases), a small Go binary —
download it and put it in your `PATH`. `rsync` ships with macOS.

Optional: one of `apex`, `glow` or `bat` for the preview pane (tried in that
order, `cat` if none are present). `twin doctor` reports what it found.

<details>
<summary>From source</summary>

```bash
git clone https://github.com/rhsev/mark-twin.git
cd mark-twin
gem build mark-twin.gemspec
gem install ./mark-twin-*.gem
```
</details>

## Getting started

**1. Decide where the other side lives.** `Target:` takes either form, and
neither is the special case:

```yaml
Target: admin@macbook:/Users/admin              # over ssh
Target: /Volumes/macbook/Users/admin            # a mounted volume (SMB, NFS, …)
```

Over ssh you need key-based login — twin runs `ssh -o BatchMode=yes` and never
prompts for a password, so set up `ssh-copy-id macbook` first. A mounted volume
needs no keys but has to be mounted; twin checks before every sync. The
[comparison below](#mounted-volume-or-ssh) covers the two differences that
actually matter.

**2. Point twin at a folder for sync-files**, in `~/.config/twin/config.yaml`:

```yaml
sync_dir: ~/Sync

global_excludes:
  - .DS_Store
  - .git/
```

**3. Write a sync-file**, or start from one of the [examples](examples/):

```bash
mkdir -p ~/Sync
cp examples/home.md ~/Sync/
$EDITOR ~/Sync/home.md      # adjust Source: and Target:
```

`twin add ~/.config/fish` does the same interactively, if you prefer prompts to
an editor — it finds the matching sync-file, derives the relative path,
suggests excludes for what it sees in the directory, and appends a block with a
prose stub.

**4. Look before you leap:**

```bash
twin status              # what differs
twin sync --dry-run      # what a sync would do, without doing it
twin                     # interactive: pick a program, pick paths, Enter
```

That is the whole loop. Everything below is detail you can come back for.

## Everyday commands

```bash
twin                         # picker — all programs across all sync-files
twin home.md                 # picker — one sync-file in sync_dir (by name)
twin ./some/dir/             # picker — all sync-files in a directory
twin list                    # plain listing
twin status                  # listing with source/target mtimes
twin sync -p grubber         # sync one program by name pattern
twin sync --file=repos       # sync all programs from a sync-file
twin sync --dry-run          # preview without writing
twin sync -v                 # rsync's full output instead of just the changes
twin log                     # recent journal entries (-n N, --json)
twin doctor                  # check tools, renderers, and targets
twin --version               # which twin is actually running
twin --help                  # show usage
```

A file argument without `/` is matched by substring against sync-file names in
`sync_dir`; anything containing `/` is treated as a path, file or directory.

By default a sync prints only what changed — the lines below, plus anything a
`Cmd` produced and any error. `-v` adds rsync's headers and transfer summaries
back. The bare `twin` picker needs a terminal and says so instead of waiting
when there is none, so cron jobs and `ssh host twin …` fail with a usable
message rather than hanging.

Every job is journaled to `~/.local/state/twin/log.jsonl` — one JSON line with
timestamp, program, path and outcome. `twin sync` exits non-zero if any job
failed.

### Reading a dry-run

`--dry-run` passes rsync's `--itemize-changes` through, which is precise but
terse. The first two characters are what matter:

| Line | Means |
|---|---|
| `>f+++++++++` | new file, does not exist on the target yet |
| `>f.s.......` | `s` is set: the size differs, so the content really changed |
| `>f..t......` | only `t`: same size, later timestamp — usually identical bytes |
| `.f...p.....` | no transfer at all, permissions only |
| `.d..tp.....` | a directory's own timestamp or permissions |
| `*deleting` | removed on the target (`Delete: true` jobs) |

Two rules cover most of it: a leading `>` means data would move and a leading
`.` means it would not, and among the flags `s` is the one that proves the
content changed rather than just a timestamp.

Only the `>` and `*deleting` lines appear by default — the `.` ones are what
`-v` adds back, along with rsync's headers and byte counts. So a job that
prints nothing under its name moved nothing, and a run full of `>f..t......`
lines is twin re-stamping files whose contents already match, which is normal
after syncing a tree in both directions.

## Sync-files

One Markdown file per relationship. Frontmatter sets it up, YAML blocks define
the individual paths. Frontmatter fields are merged into every block, so
`Source:`/`Target:` are usually written once at the top — but a block may
override them, which is how one file can serve several destinations.

Blocks sharing a `Program` are grouped: they are selected together, synced
together, and **run in the order they appear in the file**.

### Field reference

Field names are capitalised English. An unknown key is ignored silently and a
missing `Active` counts as `0`, so a typo shows up as an entry that never syncs
rather than as an error.

| Field | Where | Meaning |
|---|---|---|
| `Program` | block | Group name; blocks sharing it sync together |
| `Path` | block | Path relative to `Source` (file or directory) |
| `Source` | either | Absolute base path on this machine |
| `Target` | either | Absolute base path, or `user@host:/path` for ssh |
| `Target-Path` | block | Path under `Target`, when it differs from `Path` |
| `Active` | either | `1` syncs, `0` skips. Default `0` |
| `Description` | block | Shown in listings and the picker |
| `Label` | either | Free-text grouping, filterable via `--label` |
| `Exclude` | block | Comma-separated paths that are not part of the sync |
| `Own` | block | Comma-separated paths the **target** owns |
| `Delete` | block | `true` mirrors deletions, with backups |
| `Cmd` | block | Shell command, run only when bytes actually moved |
| `Render` | block | `true` substitutes `{{tokens}}` instead of copying |

### Exclude or Own?

Both keep rsync away from a path and both take a comma-separated list
(`Exclude: *.log, __pycache__/`). What differs is the meaning, and `twin status`
reports them apart:

- **`Exclude:`** — not part of this sync at all. Build artefacts, logs, caches,
  `.git/`, a test script with no business on the other machine.
- **`Own:`** — inside the sync scope, but the **target** owns it.
  Machine-specific configuration the source must never clobber:
  `conf.d/local.fish`, `lazy-lock.json`, a per-host credentials file.

The distinction is documentation, not mechanism. Six months on, `Own:` still
says "deliberate, the other machine maintains this", where the same entry sitting
in `Exclude:` between `*.dwarf` and `.DS_Store` reads like noise you once
filtered out.

### Cmd: doing something after a sync

`Cmd` runs a shell command once rsync has actually transferred bytes — a no-op
sync runs nothing. Typically a `curl` to a local automation endpoint like
[mi.lan](https://github.com/rhsev/mi.lan), or an `ssh host '…'` for remote
targets (`Cmd` always runs locally).

It belongs to a block rather than to the program, which is what you want when
different paths need different follow-ups — reload nginx after its config,
re-link binaries after `bin/`. For a **restart**, put the command in the
**last** block: jobs run in file order, so a restart placed earlier brings the
service back before the remaining paths are written. The same command repeated
across blocks restarts repeatedly, for the same reason. One command, last block.

### Delete: mirroring removals

`Delete: true` adds `--delete`, so files removed from the source disappear on
the target too. Deleted and overwritten files are moved to
`<target>/.twin-backup/<timestamp>/` rather than destroyed — a safety net worth
pruning occasionally. It applies to `Delete` jobs only.

## Mounted volume or ssh

Both are first-class. `twin status`, the picker, `Exclude`/`Own`, `Delete` and
`Cmd` behave identically; remote paths are stat'ed in a single ssh round-trip
per host, and an unreachable host shows as `?` instead of failing the scan.

Two differences are real:

|  | mounted volume | ssh |
|---|---|---|
| Setup | volume must be mounted | ssh key (`ssh-copy-id`) |
| Before syncing | mount check | reachability check |
| `Render: true` | supported | **not** supported |

`Render` needs to read and write file contents on the target, which twin only
does locally. Keep rendered files on mounted targets, or render locally and sync
the result.

Syncing is push-only in both cases: `Source:` is always this machine.

```markdown
---
Active: 1
Label: mini → server
Source: /Volumes/lightning/Git/Website
Target: ralf@server:/srv/www
---
```

## Two shapes of sync

The mechanics are the same either way, but what you *mean* differs, and it
decides how you should answer everything below.

**A mirror.** Both machines are yours, both get worked on, and each direction
is its own entry with `Source:` and `Target:` swapped. Neither side is more
right than the other; a file being newer over there is ordinary, and the
question is which version you want. This is what the two-Macs examples in this
README describe.

**A deploy.** One side is the truth and the other only runs it — a server, a
container, a NAS. The rule that makes this work is short: *the target is never
a source.* Nothing gets edited over there, so anything that shows up as a
target-side change means the rule was broken, and that is worth stopping for
rather than waving through.

Write the rule into the sync-file itself, in the prose where the next person —
you, in a year — will read it:

````markdown
---
Active: 1
Label: mini → dylan
Source: /Volumes/lightning/Git/rhsev/dy.lan
Target: /Volumes/docker/dylan
---

# Dylan

Deploy to the container. **Work happens locally; the target is never a
source.** A `target_newer` in `twin status` is therefore not a normal
state — find out who edited over there before overwriting it.
````

The practical difference is which answer you pre-arrange for automation. On a
mirror there is rarely a right answer in advance, so run those syncs by hand,
or with `--skip-conflicts` and read the log. On a deploy `--force` matches the
model — the source *is* the truth — but it discards a target-side edit without
showing it to you first. That is precisely the trade the prompt exists to make
deliberate, so prefer `--skip-conflicts` for scheduled runs and keep `--force`
for the moment you have looked and decided.

## When the target has changed too

A sync has a direction: the source wins. But targets get edited — a quick fix
made on the server at midnight, a config tweaked where it runs. Twin looks for
that before it moves the first byte, and asks once for the whole program:

```
target has changed since the last sync — 1 file(s) differ:
  ! app/code.rb (target 2h newer)
syncing would replace them with the source version.

overwrite these on the target and sync? [y]es / [d]iff / [n]o (abort)
```

`d` prints a unified diff per file, then asks again. `n` aborts the entire
program — nothing is written, so you never end up with half a deploy applied.

Two properties make this bearable day to day:

- **Content, not timestamps.** A file that is merely newer on the target with
  identical bytes is not a conflict and does not ask. Sync a tree in both
  directions and you collect dozens of those; a prompt that fires on them gets
  answered without being read.
- **Directory mtimes are ignored.** Editing a file in place leaves its
  directory's mtime untouched, and `rsync -a` equalises those anyway. Twin asks
  rsync what it would actually transfer instead of guessing from a directory.

`twin status` is still mtime-based and cannot see an in-place edit. It is the
cheap overview; `twin sync` is what decides.

## Automation

For a scheduled run (launchd, cron):

```bash
twin sync --quiet --skip-unavailable --skip-conflicts
```

- `--quiet` — only conflicts, errors and jobs that changed something. A no-op
  run is silent.
- `--skip-unavailable` — a laptop that isn't docked is skipped, not an error.
- `--skip-conflicts` — leave target-side changes alone and sync the rest.
  Use `--force` instead to overwrite them.

Without a terminal and without one of those two flags, a real conflict aborts
the run with exit code 1 rather than picking an answer for you. Output and a
non-zero exit therefore mean something genuinely needs attention — which is
what launchd's logging wants. The journal records every job regardless.

## Templating

*Skip this until you hit the problem it solves.*

Some configs differ per machine — a LaunchAgent plist pointing at
`/Volumes/lightning/…` on one Mac and `/Users/ralf/…` on another. Those used to
fall out of twin and get hand-maintained.

Define a host table in `~/.config/twin/config.yaml`:

```yaml
host: mini          # which machine twin runs on
target: book        # the machine being synced to

hosts:
  mini: { home: /Volumes/lightning/users/extern, git: /Volumes/lightning/Git }
  book: { home: /Users/ralf, git: /Users/ralf/git, mount: /Volumes/ralf }
```

That exposes three sets of `{{tokens}}`, each with one fixed meaning:

| Token | Resolves to | Use in |
|---|---|---|
| `{{src.home}}`, `{{src.git}}`, … | the running host's own paths | `Source:` (read side) |
| `{{dst.mount}}` | where the target is mounted here (`/Volumes/ralf`) | `Target:` (write side) |
| `{{dst.home}}`, `{{dst.git}}`, … | the target's *native* paths | rendered file **content** |

The distinction matters: a file *written* to the mount (`/Volumes/ralf/…`) but
*read* by the target machine must contain that machine's native paths
(`/Users/ralf/…`). `{{dst.mount}}` and `{{dst.home}}` keep the two apart.

> **Quote templated values.** `{{` at the start of a YAML value collides with
> YAML flow-mapping syntax, so write `Source: "{{src.home}}"`, not
> `Source: {{src.home}}` — exactly as in Ansible.

`Render: true` turns a block from copy into *render*: twin reads the source as a
template, substitutes `{{…}}` in its **content**, and writes the result only if
it differs from the current target, so a `Cmd` hook fires only on a real change.
`Target-Path:` overrides the target-side relative path:

````markdown
## LiveSync LaunchAgent

```yaml
Program: livesync-agent
Source: "{{src.home}}/Automation/launchd"
Path: com.ralf.livesync.plist
Target: "{{dst.mount}}"
Target-Path: Library/LaunchAgents/com.ralf.livesync.plist
Render: true
Cmd: curl -s http://mi.lan/livesync-reload
```
````

`twin doctor` checks that every `{{token}}` resolves, and `twin status` compares
rendered output by content rather than mtime. Without a `hosts` table,
templating is inert and literal-path sync-files behave exactly as before.

## Configuration

`~/.config/twin/config.yaml`:

```yaml
sync_dir: /path/to/sync-files

global_excludes:
  - .DS_Store
  - .git/

# Optional preview rendering (apex):
# apex_theme: default
# apex_width: 80
# apex_code_highlight: monokai
# apex_code_highlight_theme: dark
```

Environment overrides: `TWIN_SYNC_DIR`, `TWIN_CONFIG`, `TWIN_HOST` (which host
twin runs as — lets one config serve both machines).

## Design

Sync instructions and their context in one place. No TUI framework: `fzf` does
the interactive part, `apex` the rendering, `rsync` the work.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the data model and internals.

## Tests

```bash
rake test
```

---

*Part of a family of plain-text tools — the [profile page](https://github.com/rhsev) has the map.*
