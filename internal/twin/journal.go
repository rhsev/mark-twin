package twin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The journal is append-only: one JSON line per synced job in
// ~/.local/state/twin/log.jsonl. It answers "did yesterday's sync actually
// run, and what did it do?" — and stays machine-readable (jq/grubber).
// Journal failures never break a sync; they degrade to a warning.

// JournalEntry is one line of the journal.
type JournalEntry struct {
	TS      string  `json:"ts"`
	Program string  `json:"program"`
	Path    string  `json:"path"`
	Target  string  `json:"target"`
	OK      bool    `json:"ok"`
	Changed bool    `json:"changed"`
	Error   *string `json:"error,omitempty"`
}

// StateDir is $TWIN_STATE_DIR or ~/.local/state/twin.
func StateDir() string {
	if v := os.Getenv("TWIN_STATE_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "twin")
}

// LogPath is the journal file.
func LogPath() string { return filepath.Join(StateDir(), "log.jsonl") }

var journalWarned bool

// RecordJournal appends one job result. Dry-runs are not journaled — the
// caller decides.
func RecordJournal(job *Job, success, transferred bool, output string) {
	e := JournalEntry{
		TS:      time.Now().Format(time.RFC3339),
		Program: job.Program,
		Path:    job.Path,
		Target:  job.Target,
		OK:      success,
		Changed: transferred,
	}
	if !success {
		msg := lastLine(output)
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200])
		}
		e.Error = &msg
	}
	err := appendJournal(e)
	if err != nil && !journalWarned {
		fmt.Fprintf(os.Stderr, "journal: %v\n", err)
		journalWarned = true
	}
}

func appendJournal(e JournalEntry) error {
	if err := os.MkdirAll(StateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(LogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := MarshalJSON(e, "")
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// lastLine is the last non-empty trimmed line of output.
func lastLine(output string) string {
	last := ""
	for _, l := range strings.Split(output, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			last = t
		}
	}
	return last
}

// JournalTail returns the last n entries, oldest first. Unparseable lines
// are skipped but still count towards n.
func JournalTail(n int) []JournalEntry {
	data, err := os.ReadFile(LogPath())
	if err != nil {
		return []JournalEntry{}
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	entries := []JournalEntry{}
	for _, l := range lines {
		var e JournalEntry
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

// MarshalJSON encodes like Ruby's JSON.generate/pretty_generate: no HTML
// escaping, two-space indent when indent is given, no trailing newline.
func MarshalJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if out == nil {
		return nil, errors.New("empty encoding")
	}
	return out, nil
}
