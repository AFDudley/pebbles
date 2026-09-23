package pebbles

import (
	"strings"
	"testing"
)

// oversizedCommentLine builds one well-formed comment-event JSONL line
// (no trailing newline) of exactly size bytes, by padding the comment body.
func oversizedCommentLine(t *testing.T, issueID string, size int) []byte {
	t.Helper()
	base := []byte(`{"type":"comment","timestamp":"2026-01-01T00:00:00Z","issue_id":"` + issueID + `","payload":{"body":""}}`)
	baseLen := len(base)
	padding := size - baseLen
	if padding < 0 {
		t.Fatalf("size %d is smaller than the minimum line size %d", size, baseLen)
	}
	line := `{"type":"comment","timestamp":"2026-01-01T00:00:00Z","issue_id":"` + issueID + `","payload":{"body":"` + strings.Repeat("x", padding) + `"}}`
	if len(line) != size {
		t.Fatalf("built line of %d bytes, expected %d", len(line), size)
	}
	return []byte(line)
}

// TestSplitEventLinesReportsOversizedLineByNumber pins the split itself:
// a line over maxEventLineBytes is named in oversized, not parsed into
// lines, and every other line is still returned.
func TestSplitEventLinesReportsOversizedLineByNumber(t *testing.T) {
	ok := []byte(`{"type":"create"}`)
	over := oversizedCommentLine(t, "pb-1", maxEventLineBytes+1)
	data := append(append(append([]byte{}, ok...), '\n'), append(over, '\n')...)
	data = append(data, ok...)

	lines, oversized := splitEventLines(data)
	if len(lines) != 2 {
		t.Fatalf("expected 2 readable lines, got %d", len(lines))
	}
	if lines[0].Number != 1 || lines[1].Number != 3 {
		t.Fatalf("expected line numbers 1 and 3, got %d and %d", lines[0].Number, lines[1].Number)
	}
	if len(oversized) != 1 {
		t.Fatalf("expected 1 oversized line, got %d", len(oversized))
	}
	if oversized[0].Line != 2 {
		t.Fatalf("expected oversized line 2, got %d", oversized[0].Line)
	}
	if oversized[0].Size != maxEventLineBytes+1 {
		t.Fatalf("expected oversized size %d, got %d", maxEventLineBytes+1, oversized[0].Size)
	}
}

// TestSplitEventLinesAcceptsExactlyMaxSize pins the boundary: a line of
// exactly maxEventLineBytes is not oversized.
func TestSplitEventLinesAcceptsExactlyMaxSize(t *testing.T) {
	line := oversizedCommentLine(t, "pb-1", maxEventLineBytes)
	lines, oversized := splitEventLines(append(line, '\n'))
	if len(oversized) != 0 {
		t.Fatalf("expected no oversized lines at exactly the max size, got %d", len(oversized))
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
}

// TestReadEventLogSkipsOversizedLineAndReportsIt reproduces so-a5d clause
// c4: a line over maxEventLineBytes no longer fails the whole read (the old
// bufio.Scanner default failed on far smaller lines, 64 KiB). It is instead
// named by line number, and the rest of the log still comes back.
func TestReadEventLogSkipsOversizedLineAndReportsIt(t *testing.T) {
	root := seedProject(t, "pb-oversized")
	appendRaw(t, root, append(oversizedCommentLine(t, "pb-oversized", maxEventLineBytes+1024), '\n'))
	if err := AppendEvent(root, NewStatusEvent("pb-oversized", "in_progress", "2026-01-01T00:01:00Z")); err != nil {
		t.Fatalf("append status: %v", err)
	}

	entries, oversized, err := LoadEventLog(root)
	if err != nil {
		t.Fatalf("LoadEventLog failed on an oversized line instead of skipping it: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected the seed create and the trailing status entry, got %d entries", len(entries))
	}
	if len(oversized) != 1 || oversized[0].Line != 2 {
		t.Fatalf("expected line 2 reported oversized, got %+v", oversized)
	}
}

// TestLoadEventsSkipsOversizedLine reproduces so-a5d clauses c1-c3: an
// oversized comment line must not make the rest of the bus unreadable
// through the cache-feeding LoadEvents/decodeEvents path.
func TestLoadEventsSkipsOversizedLine(t *testing.T) {
	root := seedProject(t, "pb-oversized-2")
	appendRaw(t, root, append(oversizedCommentLine(t, "pb-oversized-2", maxEventLineBytes+1024), '\n'))
	if err := AppendEvent(root, NewStatusEvent("pb-oversized-2", "in_progress", "2026-01-01T00:01:00Z")); err != nil {
		t.Fatalf("append status: %v", err)
	}

	events, err := LoadEvents(root)
	if err != nil {
		t.Fatalf("LoadEvents failed on an oversized line instead of skipping it: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected the seed create and the trailing status event, got %d events", len(events))
	}
}
