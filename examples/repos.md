---
Active: 1
Label: mac-mini → macbook (repos)
Source: /Users/admin/Git
Target: /Volumes/macbook/Users/admin/Git
---

# Git repositories

Active working copies that should mirror between the two Macs. Build
artefacts and per-repo caches are excluded via the global config; anything
repo-specific is handled below.

## grubber

The grubber Go source tree.

```yaml
Program: grubber
Path: rhsev/grubber
Description: Markdown/YAML block extractor (Go)
Exclude: build/, vendor/
```

## grubber-twin

This project. Tests stay; `.gem` artefacts are filtered globally.

```yaml
Program: grubber-twin
Path: rhsev/grubber-twin
Description: Sync CLI built on grubber
```

## Notes

Personal notes repository. Larger attachments excluded.

```yaml
Program: Notes
Path: notes
Description: Personal markdown notes
Exclude: attachments/large/
```
