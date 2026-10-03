package twin

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// `twin add <path>` — guided scaffolding of a new sync entry. Turns the
// judgment calls of the "add a sync" recipe (which sync-file? which Path?
// what to exclude? deploy hook?) into prompts with sensible defaults, then
// appends a Markdown block to the chosen sync-file.

// SuggestExcludeDirs are usually machine-generated or heavy — offered as
// exclude defaults when present in the source directory.
var SuggestExcludeDirs = []string{".git", "node_modules", ".venv", "__pycache__", "dist", "build", "target"}

var frontmatterEnd = regexp.MustCompile(`(?m)^---\s*$`)

// Frontmatter reads a sync-file's frontmatter as a map (Source/Target
// token-substituted), or nil when the file has none / it isn't a mapping.
func Frontmatter(file string, vars map[string]string) (map[string]any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, nil
	}
	body := frontmatterEnd.Split(text[4:], 2)[0]
	var fm map[string]any
	if yaml.Unmarshal([]byte(body), &fm) != nil || fm == nil {
		return nil, nil
	}
	for _, k := range []string{"Source", "Target"} {
		s, ok := fm[k].(string)
		if !ok {
			continue
		}
		sub, err := Substitute(s, vars, filepath.Base(file))
		if err != nil {
			return nil, err
		}
		fm[k] = sub
	}
	return fm, nil
}

// Candidate is a sync-file whose Source covers a path.
type Candidate struct {
	File        string
	Frontmatter map[string]any
}

// Candidates lists the sync-files in dir whose Source is an ancestor of
// path.
func Candidates(dir, path string, vars map[string]string) ([]Candidate, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	sort.Strings(files)
	var out []Candidate
	for _, f := range files {
		fm, err := Frontmatter(f, vars)
		if err != nil {
			return nil, err
		}
		if fm == nil {
			continue
		}
		src, ok := fm["Source"].(string)
		if !ok || src == "" {
			continue
		}
		root := ExpandPath(src)
		if path == root || strings.HasPrefix(path, root+"/") {
			out = append(out, Candidate{File: f, Frontmatter: fm})
		}
	}
	return out, nil
}

// RelativePath is path relative to root, "." for root itself.
func RelativePath(root, path string) string {
	if path == root {
		return "."
	}
	return strings.TrimPrefix(path, root+"/")
}

// SuggestExcludes lists the heavy directories present under path.
func SuggestExcludes(path string) []string {
	out := []string{}
	if !isDir(path) {
		return out
	}
	for _, e := range SuggestExcludeDirs {
		full := filepath.Join(path, e)
		if !exists(full) {
			continue
		}
		if isDir(full) {
			out = append(out, e+"/")
		} else {
			out = append(out, e)
		}
	}
	return out
}

// BuildBlock renders the Markdown block for one sync entry.
func BuildBlock(program, path, description string, excludes []string, delete bool, cmd, prose string) string {
	lines := []string{"Program: " + program, "Path: " + path}
	if description != "" {
		lines = append(lines, "Description: "+description)
	}
	if len(excludes) > 0 {
		lines = append(lines, "Exclude: "+strings.Join(excludes, ","))
	}
	if delete {
		lines = append(lines, "Delete: true")
	}
	if cmd != "" {
		lines = append(lines, "Cmd: "+cmd)
	}
	if prose == "" {
		prose = "TODO: document why this path is synced."
	}
	return fmt.Sprintf("\n## %s\n\n%s\n\n```yaml\n%s\n```\n", program, prose, strings.Join(lines, "\n"))
}

// FrontmatterText renders the frontmatter of a new sync-file.
func FrontmatterText(source, target, label string) string {
	lines := []string{"---", "Active: 1"}
	if label != "" {
		lines = append(lines, "Label: "+label)
	}
	lines = append(lines, "Source: "+source, "Target: "+target, "---", "")
	return strings.Join(lines, "\n")
}

// AddResult says what `twin add` wrote and whether a dry-run was asked for.
type AddResult struct {
	Program string
	File    string
	DryRun  bool
}

// Prompter asks the user questions on a terminal.
type Prompter struct {
	In  *bufio.Reader
	Out io.Writer
}

// Ask prints a prompt and returns the answer, or the default when empty.
func (p *Prompter) Ask(prompt, def string) (string, error) {
	if def == "" {
		fmt.Fprintf(p.Out, "%s: ", prompt)
	} else {
		fmt.Fprintf(p.Out, "%s [%s]: ", prompt, def)
	}
	line, err := p.In.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("aborted (stdin closed)")
	}
	ans := strings.TrimSpace(line)
	if ans == "" {
		return def, nil
	}
	return ans, nil
}

// Yes is true for answers starting with y/Y.
func Yes(answer string) bool {
	return strings.HasPrefix(strings.ToLower(answer), "y")
}

// RunAdd is the interactive flow. It returns nil when nothing was written.
func RunAdd(cfg *Config, args []string, p *Prompter) (*AddResult, error) {
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("usage: twin add <path>")
	}
	path := ExpandPath(args[0])
	if !exists(path) {
		return nil, fmt.Errorf("not found: %s", path)
	}

	vars, err := cfg.VarMap()
	if err != nil {
		return nil, err
	}
	picked, err := pickSyncFile(cfg, path, vars, p)
	if err != nil || picked == nil {
		return nil, err
	}
	file, fm := picked.File, picked.Frontmatter

	root := ExpandPath(str(fm["Source"]))
	rel := RelativePath(root, path)

	if data, err := os.ReadFile(file); err == nil {
		dup := regexp.MustCompile(`(?m)^Path:\s*` + regexp.QuoteMeta(rel) + `\s*$`)
		if dup.Match(data) {
			return nil, fmt.Errorf("%s already has a block with Path: %s", filepath.Base(file), rel)
		}
	}

	program, err := p.Ask("Program name", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	prose, err := p.Ask("Why is this synced? (one line of prose)", "")
	if err != nil {
		return nil, err
	}
	desc, err := p.Ask("Description (short, for listings)", program)
	if err != nil {
		return nil, err
	}
	exclAns, err := p.Ask("Exclude (comma-separated)", strings.Join(SuggestExcludes(path), ","))
	if err != nil {
		return nil, err
	}
	excl := SplitList(exclAns)
	delAns, err := p.Ask("Mirror deletions on target (Delete: true)? (y/N)", "n")
	if err != nil {
		return nil, err
	}
	cmd, err := p.Ask("Post-sync Cmd (empty for none)", "")
	if err != nil {
		return nil, err
	}

	block := BuildBlock(program, rel, desc, excl, Yes(delAns), cmd, prose)
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(block); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	fmt.Fprintf(p.Out, "\nadded %q to %s\n", program, filepath.Base(file))
	fmt.Fprintf(p.Out, "  %s  →  %s\n", joinPath(root, rel), joinPath(str(fm["Target"]), rel))

	dryAns, err := p.Ask("Run a dry-run now? (Y/n)", "y")
	if err != nil {
		return nil, err
	}
	return &AddResult{Program: program, File: file, DryRun: Yes(dryAns)}, nil
}

// pickSyncFile chooses (or creates) the sync-file covering path.
func pickSyncFile(cfg *Config, path string, vars map[string]string, p *Prompter) (*Candidate, error) {
	cands, err := Candidates(cfg.SyncDir, path, vars)
	if err != nil {
		return nil, err
	}
	switch len(cands) {
	case 0:
		return offerNewSyncFile(cfg, path, vars, p)
	case 1:
		c := cands[0]
		fmt.Fprintf(p.Out, "sync-file: %s  (%s → %s)\n", filepath.Base(c.File),
			str(c.Frontmatter["Source"]), str(c.Frontmatter["Target"]))
		return &c, nil
	}
	fmt.Fprintf(p.Out, "multiple sync-files cover %s:\n", path)
	for i, c := range cands {
		fmt.Fprintf(p.Out, "  %d) %s  (%s → %s)\n", i+1, filepath.Base(c.File),
			str(c.Frontmatter["Source"]), str(c.Frontmatter["Target"]))
	}
	ans, err := p.Ask("Which one?", "1")
	if err != nil {
		return nil, err
	}
	n := toInt(ans)
	if n < 1 || n > len(cands) {
		return nil, fmt.Errorf("invalid choice: %d", n)
	}
	return &cands[n-1], nil
}

func offerNewSyncFile(cfg *Config, path string, vars map[string]string, p *Prompter) (*Candidate, error) {
	fmt.Fprintf(p.Out, "no sync-file in %s covers %s\n", cfg.SyncDir, path)
	ans, err := p.Ask("Create a new sync-file? (y/N)", "n")
	if err != nil || !Yes(ans) {
		return nil, err
	}

	name, err := p.Ask("File name", strings.TrimPrefix(filepath.Base(path), ".")+".md")
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	file := filepath.Join(cfg.SyncDir, name)
	if exists(file) {
		return nil, fmt.Errorf("already exists: %s", file)
	}

	source, err := p.Ask("Source base on this machine", filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	target, err := p.Ask("Target base (mount path or user@host:/path)", "")
	if err != nil {
		return nil, err
	}
	if target == "" {
		return nil, fmt.Errorf("Target is required")
	}
	label, err := p.Ask("Label (e.g. mini → server)", "")
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(file, []byte(FrontmatterText(source, target, label)), 0o644); err != nil {
		return nil, err
	}
	fmt.Fprintf(p.Out, "created %s\n", filepath.Base(file))
	fm := map[string]any{}
	for k, v := range map[string]string{"Source": source, "Target": target} {
		sub, err := Substitute(v, vars, name)
		if err != nil {
			return nil, err
		}
		fm[k] = sub
	}
	return &Candidate{File: file, Frontmatter: fm}, nil
}
