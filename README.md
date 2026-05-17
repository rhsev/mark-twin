# twin

Sync configuration folders between two Macs from self-documenting Markdown files.

Sync entries are defined in Markdown files with YAML blocks — human-readable,
self-documenting, and queryable via [grubber](https://github.com/rhsev/grubber).
Selection is interactive via [fzf](https://github.com/junegunn/fzf), with a
Markdown preview rendered by [apex](https://github.com/ttscoff/apex).

## Why?

Sync-definitions in Markdown + YAML are three things at once:

- **Human-readable.** Plain Markdown, no twin-specific syntax to learn. The
  Markdown frame documents *why* a path is synced, not just what, so you can
  read your own sync-files in a year and still understand them.
- **Machine-readable.** The YAML blocks are queryable via grubber, so any tool
  (twin, but also future ones) can act on the same source of truth.
- **AI-writable.** LLMs handle Markdown + YAML well. You can ask an assistant to
  add new entries or refactor existing ones, and the result stays valid for both
  humans and grubber.

## Usage

```bash
twin                         # picker — all programs across all sync-files
twin home_macbook.md         # picker — one sync-file in sync_dir (by name)
twin /abs/path/to/file.md    # picker — any sync-file by absolute path
twin ./relative/dir/         # picker — all sync-files in a directory
twin list                    # plain listing
twin status                  # listing with source/target mtimes
twin sync -p grubber         # sync one program by name pattern
twin sync --file=repos       # sync all programs from a sync-file
twin sync --dry-run          # preview without writing
twin --help                  # show usage
```

File argument resolution:

- bare name (no `/`)  → looked up by substring in `sync_dir`
- contains `/`        → resolved as path (absolute or relative); file or directory both work

## Installation

```bash
gem install grubber-twin
```

Or from source:

```bash
git clone https://github.com/rhsev/grubber-twin.git
cd grubber-twin
gem build grubber-twin.gemspec
gem install ./grubber-twin-*.gem
```

External tools required in PATH: `grubber`, `rsync`, `fzf`. For the
stage-2 preview, one of `apex`, `glow`, or `bat` (falls back in that order;
`cat` if none are present).

## Configuration

`~/.config/twin/config.yaml`:

```yaml
sync_dir: /path/to/sync-files

global_excludes:
  - .DS_Store
  - .git/

apex_theme: ralf       # optional
apex_width: 80         # optional
```

Environment overrides: `TWIN_SYNC_DIR`, `TWIN_CONFIG`.

## Sync-files

Each Markdown file represents one sync relationship. Frontmatter defines the
relationship; YAML blocks define individual paths.

Example:

````markdown
---
Active: 1
Label: mac-mini→macbook
Source: /Users/admin
Target: /Volumes/macbook/Users/admin
---

## Fish Shell

Configuration for the fish shell, including completions and abbreviations.

```yaml
Program: Fish Shell
Path: .config/fish
Description: Fish Shell configuration
Exclude: conf.d/local.fish
```
````

Frontmatter fields (`Active`, `Label`, `Source`, `Target`) are merged into
every block by grubber. Multiple blocks can share the same `Program` — twin
groups them and treats the program as the unit of selection.

The optional `Cmd` field runs a shell command after a successful sync.

## Design

- **Selection unit:** Program. A program may have several blocks (paths); the
  picker shows one row per program.
- **Preview:** the whole sync-file rendered with `apex --plugins -t terminal256`.
- **No TUI framework:** `fzf` does the interactive part, `apex` the rendering.
  Composition over framework.

See `ARCHITECTURE.md` for details.

## Tests

```bash
rake test
```
