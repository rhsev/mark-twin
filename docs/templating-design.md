# Templating — Implementation Spec (IDEAS #1)

Status: **implemented & verified** (both layers, end-to-end through grubber).
Module `lib/twin/template.rb`; touches config/scanner/sync/cli. Scope of first
round: path-field substitution + render-then-place content rendering.

The `transferred?` no-op bug (#2 fired `Cmd` on every run) is **already fixed**
on this branch — don't redo it.

## Goal

Manage machine-specific config files from one source of truth. Today files
whose *path or content* differs per host fall out of twin and are hand-kept
(see the `local.fish` excludes in `Automation/sync/home_macbook.md`). Templating
folds them back in.

## Two problems, two mechanisms

| | Problem | Mechanism |
|---|---|---|
| **A. Path fields** | `Source:`/`Target:` differ per host pair | string substitution in field values |
| **B. File content** | the plist *itself* contains `/Volumes/lightning/…`, must read `/Users/ralf/…` on the book | **render-then-place**: read template → substitute → write |

rsync copies bytes — it cannot do B. A plist with mini paths lands unchanged on
the book and is wrong there. That is the real reason these files are excluded today.

## Three namespaces (the crux)

Every sync involves three views of a path. Mount ≠ native:

```
mini home    /Volumes/lightning/users/extern   ← src native   (read side)
book mount   /Volumes/ralf                      ← book as mini sees it (write side)
book native  /Users/ralf                        ← book as book sees itself (content!)
```

A rendered plist must *contain* `/Users/ralf/…` (launchd reads it on the book)
but is *written* to `/Volumes/ralf/…`. One token must never mean different
things in a path field vs. in content — so every variable carries an explicit
prefix.

## Variable reference

| Token | Resolves to | Use in |
|---|---|---|
| `{{src.home}}`, `{{src.git}}`, … | source host native | `Source:` (read side) |
| `{{dst.mount}}` | target's mount root on source (`/Volumes/ralf`) | `Target:` (write side) |
| `{{dst.home}}`, `{{dst.git}}`, … | target host native | rendered file **content** |

`src.*`/`dst.*` keys come from the host table; `dst.mount` from the target
host's `mount:` field. Substitution is plain string replacement — no logic, no
conditionals. **An unknown `{{…}}` token is a hard error** (never sync a
half-rendered path).

### Gotcha: quote templated YAML values

`{{` at the start of a YAML scalar collides with YAML flow-mapping syntax (`{…}`)
— grubber (and any YAML parser) fails to parse the file before twin ever sees
it. **Templated values must be quoted**, exactly as in Ansible:

```yaml
Source: "{{src.home}}"          # ✓ parses
Source: {{src.home}}            # ✗ YAML error: did not find expected key
```

This is verified behaviour, not theory. The worked example below quotes
accordingly.

## Config schema (config.rb)

```yaml
sync_dir: /Volumes/lightning/users/extern/Automation/sync

host: mini          # which machine twin runs on  (key into hosts; TWIN_HOST overrides)
target: book        # default target               (a --target flag is future work, #7)

hosts:
  mini: { home: /Volumes/lightning/users/extern, git: /Volumes/lightning/Git }
  book: { home: /Users/ralf, git: /Users/ralf/git, mount: /Volumes/ralf }
```

- `mount` is the only mount assumption: a single prefix, valid because the SMB
  share *is* the home dir (`/Volumes/ralf` == book's `/Users/ralf`). It scales
  to a third machine without duplicating sync-files (#7).
- New `Config` accessors: `hosts`, `host`, `target`. Add a resolver that, given
  source+target host names, produces the flat var map
  `{ "src.home" => …, "dst.home" => …, "dst.mount" => … }`.
- Backward compatibility is **not** required (tool is young, not widespread) —
  pick the clean schema, don't bend for old files. In practice substitution is a
  no-op on any value without `{{…}}`, so literal-path sync-files keep working anyway.

## Sync-file fields (scanner.rb)

grubber passes through any `key: value`, so field names are ours to choose.

- `Render: true` → render-then-place instead of rsync (new `render:` Job field,
  exactly like the `delete:` field already added for #3).
- `Target-Path:` (optional) → overrides the **target-side** relative path,
  defaults to `Path`. Lets one block read `Automation/launchd/foo.plist` on mini
  and write `Library/LaunchAgents/foo.plist` on the book. Applies to all blocks,
  not just render — keeps one consistent grammar.
- `Job#target_path` becomes `File.join(target, target_path_field || path)`.

## Pipeline seam (scanner.rb)

```
grubber → JSON records → [SUBSTITUTE] → build_job → Job (stat/conflict) → sync
```

grubber stays untouched — it never sees `{{…}}`, twin renders on top.
Substitution **must run before `build_job`**, because `build_job` immediately
does `File.join(source, path)` + `stat` for conflict detection; a path with
literal braces does not exist on disk. So path-layer substitution needs **no new
Job fields** — values are already resolved by the time the Job is built. Only
Render needs the `render:` flag.

Substitute these record fields with the var map: `Source`, `Target`, `Path`,
`Target-Path`, `Exclude`, `Cmd`. (Not file content — that happens at render time
with the `dst.*` map.)

## render-then-place (sync.rb)

For `render: true` jobs, **do not rsync** — rsync's `--update`/mtime semantics
are meaningless for a freshly rendered temp file (always "newer"). Instead:

```
read source template
substitute {{dst.*}} into the bytes        (unknown token → error, abort job)
compare rendered bytes against current target file bytes
  differ?  write target, changed = true
  same?    skip,         changed = false
```

`changed` feeds the existing `Cmd` hook (the #2 logic): run `Cmd` only when
`changed`. This path sidesteps the `transferred?` heuristic entirely. Render is
**file-only** — a template is a file, never a directory; error if `source` is a dir.

Normal (non-render) jobs keep the current rsync path unchanged.

`twin status` for render jobs is **content-based**, not mtime-based: a rendered
target's mtime bears no relation to the template's, so `Job#status` renders the
template in memory and compares bytes (`render_outdated?` in scanner). Stale
content with a matching mtime is correctly flagged `source_newer` (→); identical
content is `in_sync` (✓); an unresolved token shows `source_newer` (needs
attention, never silently in_sync). Render jobs never set `conflict`.

## Error handling

- Unknown `{{token}}` in any field or rendered body → raise with the token name
  and the sync-file/program, abort before any write.
- `Render: true` with a directory source → error.
- Missing `mount:` on the target host when a value needs `{{dst.mount}}` → error.

## doctor additions (cli.rb)

Extend `twin doctor`:
- `host`/`target` resolve to entries in `hosts`.
- every `{{…}}` token across all loaded sync-files resolves in the var map
  (catches typos before a sync does).
- target host has a `mount:` if any field uses `{{dst.mount}}`.

## Worked example — the livesync plist

`home_macbook.md`:

````markdown
## LiveSync LaunchAgent

```yaml
Program: livesync-agent
Source: "{{src.home}}/Automation/launchd"
Path: com.ralf.livesync.plist
Target: "{{dst.mount}}"
Target-Path: Library/LaunchAgents/com.ralf.livesync.plist
Render: true
Cmd: curl -s http://192.168.1.195:8080/livesync-reload
```
````

The template `…/Automation/launchd/com.ralf.livesync.plist` contains
`{{dst.home}}/…` and `{{dst.git}}/…`. twin renders it to `/Users/ralf/…` paths,
writes the result to `/Volumes/ralf/Library/LaunchAgents/com.ralf.livesync.plist`,
and reloads via `Cmd` only if the rendered content changed.

## Tests to add (test/test_pure.rb)

- var resolver: host names → flat map; missing host / missing mount.
- substitution: each field; unknown token raises; no-`{{}}` value untouched.
- `Target-Path` overrides target side, defaults to `Path`.
- render: substitution into body; content-hash skip when identical; `changed`
  gates `Cmd`; directory source errors.

## Out of scope (this round)

- `{{…}}` conditionals / loops — string replace only.
- Per-key mount maps — single `mount:` prefix is enough for the SMB-home setup.
- Host auto-detection by hostname — `host:` is explicit in config for now.
