package twin

import (
	"os"
	"regexp"
	"strings"

	"github.com/rhsev/grubber/entry"
)

// Compact markdown preview for a single sync block: frontmatter + intro
// (text before the first H2) + the block's entry (heading, notes, block).

var (
	headingLine = regexp.MustCompile(`^(#{1,6})\s`)
	h2Plus      = regexp.MustCompile(`^#{2,6}\s`)
	fmSeparator = regexp.MustCompile(`(?m)^---\s*$\n`)
)

// ExtractCompact builds the compact preview for the block whose Path is
// targetPath. A missing file yields "".
func ExtractCompact(filePath, targetPath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	frontmatter, body := SplitFrontmatter(string(data))
	lines := splitLines(body)

	intro := ExtractIntro(lines)
	section := ExtractSectionFor(lines, targetPath)

	var parts []string
	for _, p := range []string{frontmatter, intro, section} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// splitLines splits like Ruby's lines.map(&:chomp).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// SplitFrontmatter separates a leading frontmatter block from the body.
func SplitFrontmatter(content string) (frontmatter, body string) {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return "", content
	}
	parts := fmSeparator.Split(content, 3)
	if len(parts) < 3 {
		return "", content
	}
	return "---\n" + parts[1] + "---", parts[2]
}

// ExtractIntro is everything up to the first H2 (H1 + body counts as the
// document title).
func ExtractIntro(lines []string) string {
	idx := len(lines)
	for i, l := range lines {
		if h2Plus.MatchString(l) {
			idx = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[:idx], "\n"))
}

// ExtractSectionFor finds the YAML block whose Path is target and returns its
// entry: the heading, the notes before the block, and the block, by the rules
// of grubber's package entry (the notes are the text between the block and
// the nearest heading, block or --- line above it; text after a block
// belongs to no entry).
func ExtractSectionFor(lines []string, target string) string {
	for _, b := range entry.ParseBlocks(lines) {
		if !b.Matches([][2]string{{"Path", target}}) {
			continue
		}
		var parts []string
		if h := headingLineFor(lines, b); h != "" {
			parts = append(parts, h)
		}
		if prose := b.Prose(lines); prose != "" {
			parts = append(parts, prose)
		}
		parts = append(parts, strings.Join(lines[b.Open:b.Close+1], "\n"))
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// headingLineFor returns the heading line of b's entry as it stands in the
// file, so the preview keeps its level.
func headingLineFor(lines []string, b entry.Block) string {
	if b.Heading == "" {
		return ""
	}
	for i := b.Open - 1; i >= 0; i-- {
		if m := headingLine.FindStringSubmatch(lines[i]); m != nil &&
			strings.TrimSpace(strings.TrimRight(strings.TrimSpace(lines[i][len(m[1]):]), "#")) == b.Heading {
			return lines[i]
		}
	}
	return ""
}
