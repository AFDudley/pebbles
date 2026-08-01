package pebbles

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// appendRaw appends raw bytes to the events log exactly as an external
// writer's in-flight write(2) would leave them — no lock, no terminating
// newline unless the caller includes one. This is the deterministic stand-in
// for "a reader parsing while a writer holds a partially-flushed final line":
// the byte state the reader observes is identical whether the writer is
// mid-write(2) or an external process that does not honor pb's flock (pebble
// so-2a4's production writers included a git merge driver and a dispatcher,
// neither of which takes .pebbles/events.jsonl.lock).
func appendRaw(t *testing.T, root string, raw []byte) {
	t.Helper()
	file, err := os.OpenFile(EventsPath(root), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open events log: %v", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(raw); err != nil {
		t.Fatalf("append raw: %v", err)
	}
}

// TestReaderToleratesInFlightFinalAppend reproduces pebble so-2a4: readers of
// events.jsonl race concurrent appenders — `pb list --all --json` failed
// transiently with "parse event: unexpected end of JSON input" on a partial
// trailing line while a full-file parse moments later found zero malformed
// lines. fd6e05a/so-b6a flocks every pb reader AND writer, but the production
// writers included non-pb processes (exophial's bus merge driver, a
// dispatcher) that never take pb's lock, so the READER itself must be correct
// under a concurrent append: exactly ONE unterminated final line is an append
// in flight — not yet part of the log — never a parse error and never
// silently lost (it appears once the terminating newline lands).
func TestReaderToleratesInFlightFinalAppend(t *testing.T) {
	root := seedProject(t, "pb-inflight")
	for i := 0; i < 3; i++ {
		event := NewCommentEvent("pb-inflight", fmt.Sprintf("note %d", i), "2026-01-01T00:01:00Z")
		if err := AppendEvent(root, event); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// Freeze the in-flight event's bytes, then land only a strict prefix of
	// its line: the exact observation a reader makes mid-append.
	inFlight := NewCommentEvent("pb-inflight", "landing right now", "2026-01-01T00:02:00Z")
	line, err := json.Marshal(inFlight)
	if err != nil {
		t.Fatalf("marshal in-flight event: %v", err)
	}
	appendRaw(t, root, line[:len(line)/2])

	// The log's settled content is the seed create plus three comments.
	events, err := LoadEvents(root)
	if err != nil {
		t.Fatalf("LoadEvents during in-flight append failed: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 settled events, got %d", len(events))
	}
	entries, err := LoadEventLog(root)
	if err != nil {
		t.Fatalf("LoadEventLog during in-flight append failed: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 settled log entries, got %d", len(entries))
	}
	// The full verb spine (EnsureCache rebuild + query) must answer, exactly
	// what `pb list --all --json` runs when the reducer's SELECT calls it.
	comments, err := ListIssueComments(root, "pb-inflight")
	if err != nil {
		t.Fatalf("read verb during in-flight append failed: %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("expected 3 settled comments, got %d", len(comments))
	}

	// The append completes: the event is not lost, merely not-yet-appended.
	appendRaw(t, root, append(line[len(line)/2:], '\n'))
	events, err = LoadEvents(root)
	if err != nil {
		t.Fatalf("LoadEvents after append completed failed: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 events once the append landed, got %d", len(events))
	}
	comments, err = ListIssueComments(root, "pb-inflight")
	if err != nil {
		t.Fatalf("read verb after append completed failed: %v", err)
	}
	if len(comments) != 4 {
		t.Fatalf("expected 4 comments once the append landed, got %d", len(comments))
	}
}

// TestMalformedInteriorLineStaysLoud pins so-2a4's boundary: tolerating an
// in-flight FINAL line must never soften interior corruption into a silent
// skip. A malformed line with settled lines after it was never an append in
// flight — it is a corrupt log, and every reader must say so.
func TestMalformedInteriorLineStaysLoud(t *testing.T) {
	root := seedProject(t, "pb-corrupt")
	appendRaw(t, root, []byte("{\"type\":\"comment\",TRUNCATED GARBAGE\n"))
	event := NewCommentEvent("pb-corrupt", "after the corruption", "2026-01-01T00:01:00Z")
	if err := AppendEvent(root, event); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := LoadEvents(root); err == nil {
		t.Fatal("LoadEvents silently skipped a malformed interior line")
	} else if !strings.Contains(err.Error(), "parse event") {
		t.Fatalf("expected a parse error naming the event, got: %v", err)
	}
	_, err := LoadEventLog(root)
	if err == nil {
		t.Fatal("LoadEventLog silently skipped a malformed interior line")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected the error to name line 2, got: %v", err)
	}
}

// TestMalformedTerminatedFinalLineStaysLoud pins the other half of the
// boundary: a final line WITH its terminating newline was fully appended —
// there is no write in flight to wait out — so garbage there is corruption,
// not tolerance territory.
func TestMalformedTerminatedFinalLineStaysLoud(t *testing.T) {
	root := seedProject(t, "pb-tailrot")
	appendRaw(t, root, []byte("{\"type\":\"comment\",TRUNCATED GARBAGE\n"))
	if _, err := LoadEvents(root); err == nil {
		t.Fatal("LoadEvents silently skipped a malformed terminated final line")
	} else if !strings.Contains(err.Error(), "parse event") {
		t.Fatalf("expected a parse error naming the event, got: %v", err)
	}
}
