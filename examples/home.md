---
Active: 1
Label: mac-mini → macbook
Source: /Users/admin
Target: /Volumes/macbook/Users/admin
---

# Home directory

Configuration and dotfiles that should stay in sync between the two Macs.
Anything machine-specific lives in a separate file or is excluded per block.

## Fish Shell

Shell configuration including completions and abbreviations. Local-only
overrides live in `conf.d/local.fish` and are excluded from sync.

```yaml
Program: Fish Shell
Path: .config/fish
Description: Fish Shell configuration
Exclude: conf.d/local.fish
```

History is synced separately so both machines see the same command history.

```yaml
Program: Fish Shell
Path: .local/share/fish/fish_history
Description: Shared shell history
```

## Helix Editor

Editor configuration: languages, themes, keymap. The `Cmd` line reloads any
open helix sessions after sync via a small companion hook.

```yaml
Program: Helix
Path: .config/helix
Description: Helix editor config
Cmd: curl -s http://mi.lan/reload-helix
```

## Git

Global git configuration and credential helper.

```yaml
Program: Git
Path: .gitconfig
Description: Global git aliases and signing config
```

## SSH

Host aliases and config. Keys are never synced (they live outside `.ssh/config`
and are excluded explicitly).

```yaml
Program: SSH
Path: .ssh/config
Description: SSH host aliases
```
