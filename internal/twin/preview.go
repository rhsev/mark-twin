package twin

import (
	"os"
	"regexp"
	"strings"
)

// Compact markdown preview for a single sync block: frontmatter + intro
// (text before the first H2) + the heading section that contains the YAML
// block for a given path.

var (
	yamlFence   = regexp.MustCompile("^```ya?ml\\s*$")
	fenceEnd    = regexp.MustCompile("^```\\s*$")
	headingLine = regexp.MustCompile(`^(#{1,6})\s`)
	h2Plus      = regexp.MustCompile(`^#{2,6}\s`)
	pathKey     = regexp.MustCompile(`^Path:\s*['"]?([^'"]+?)['"]?\s*$`)
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

// ExtractSectionFor locates the YAML block whose Path: matches target, then
// returns the surrounding heading section (nearest preceding heading until
// the next heading of equal-or-higher level).
func ExtractSectionFor(lines []string, target string) string {
	blockStart, blockEnd, ok := FindBlock(lines, target)
	if !ok {
		return ""
	}

	sectionStart := blockStart
	for i := blockStart - 1; i >= 0; i-- {
		if headingLine.MatchString(lines[i]) {
			sectionStart = i
			break
		}
	}

	sectionEnd := len(lines) - 1
	if m := headingLine.FindStringSubmatch(lines[sectionStart]); m != nil {
		level := len(m[1])
		for i := blockEnd + 1; i < len(lines); i++ {
			if m2 := headingLine.FindStringSubmatch(lines[i]); m2 != nil && len(m2[1]) <= level {
				sectionEnd = i - 1
				break
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines[sectionStart:sectionEnd+1], "\n"))
}

// FindBlock returns the fence lines of the YAML block whose Path: is target.
func FindBlock(lines []string, target string) (start, end int, ok bool) {
	i := 0
	for i < len(lines) {
		if !yamlFence.MatchString(lines[i]) {
			i++
			continue
		}
		k := i + 1
		matched := false
		for k < len(lines) && !fenceEnd.MatchString(lines[k]) {
			if m := pathKey.FindStringSubmatch(lines[k]); m != nil && m[1] == target {
				matched = true
			}
			k++
		}
		if matched {
			return i, k, true
		}
		i = k + 1
	}
	return 0, 0, false
}
