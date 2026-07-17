package pebbles

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// seedProject initialises a project with one issue and a built cache.
func seedProject(t *testing.T, issueID string) string {
	t.Helper()
	root := t.TempDir()
	if err := InitProject(root); err != nil {
		t.Fatalf("init project: %v", err)
	}
	if err := AppendEvent(root, NewCreateEvent(issueID, "Seed", "", "task", "2026-01-01T00:00:00Z", 2)); err != nil {
		t.Fatalf("append create: %v", err)
	}
	if err := RebuildCache(root); err != nil {
		t.Fatalf("rebuild cache: %v", err)
	}
	return root
}

// TestConcurrentWritersAllSucceed reproduces pebble so-b08's write half: N
// concurrent writers each append an event and refresh the cache, exactly as a
// write verb does (cmd/pb/main.go: AppendEvent then RebuildCache). Without an
// effective busy_timeout every writer but one fails with
// "database is locked (5) (SQLITE_BUSY)" instead of briefly waiting for the
// lock. Concurrency is normal for pb — several agents share one bus — so a
// concurrent write must WAIT, not error.
func TestConcurrentWritersAllSucceed(t *testing.T) {
	const writers = 8
	root := seedProject(t, "pb-seed")
	// Every writer comments on the same issue: one log, one cache, one lock.
	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			event := NewCommentEvent("pb-seed", fmt.Sprintf("note %d", index), "2026-01-01T00:01:00Z")
			if err := AppendEvent(root, event); err != nil {
				errs[index] = fmt.Errorf("append: %w", err)
				return
			}
			if err := RebuildCache(root); err != nil {
				errs[index] = fmt.Errorf("rebuild: %w", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d failed: %v", i, err)
		}
	}
	// Every appended event must be visible once the writers have finished.
	comments, err := ListIssueComments(root, "pb-seed")
	if err != nil {
		t.Fatalf("get comments: %v", err)
	}
	if len(comments) != writers {
		t.Fatalf("expected %d comments, got %d", writers, len(comments))
	}
}

// TestReadDuringWriteReturnsCorrectAnswer reproduces pebble so-b08's read half:
// a reader concurrent with an in-flight write must return a CORRECT answer —
// never "database is locked (SQLITE_BUSY)", and never a transient
// "sql: no rows in result set" from observing a cache mid-rebuild. The seeded
// issue exists in the log for the whole test, so every read must resolve it.
func TestReadDuringWriteReturnsCorrectAnswer(t *testing.T) {
	const writes = 30
	root := seedProject(t, "pb-read")
	done := make(chan struct{})
	var writeErr error
	go func() {
		defer close(done)
		for i := 0; i < writes; i++ {
			event := NewCommentEvent("pb-read", fmt.Sprintf("note %d", i), "2026-01-01T00:01:00Z")
			if err := AppendEvent(root, event); err != nil {
				writeErr = fmt.Errorf("append: %w", err)
				return
			}
			if err := RebuildCache(root); err != nil {
				writeErr = fmt.Errorf("rebuild: %w", err)
				return
			}
		}
	}()
	// Hammer the read path (EnsureCache + query) for the writer's whole run.
	reads := 0
	for {
		select {
		case <-done:
			if writeErr != nil {
				t.Fatalf("writer failed: %v", writeErr)
			}
			if reads == 0 {
				t.Fatal("no reads raced the writer")
			}
			return
		default:
		}
		issue, _, err := GetIssue(root, "pb-read")
		if err != nil {
			t.Fatalf("read %d during write failed: %v", reads, err)
		}
		if issue.Title != "Seed" {
			t.Fatalf("read %d returned wrong issue: %+v", reads, issue)
		}
		reads++
	}
}

// TestOpenDBSetsBusyTimeout asserts the busy_timeout is actually in effect on
// every connection openDB hands out — the single place so-b08's fix lives, so
// every verb inherits it. A zero timeout is the defect: SQLite then errors
// immediately on a held lock instead of waiting.
func TestOpenDBSetsBusyTimeout(t *testing.T) {
	root := seedProject(t, "pb-timeout")
	db, err := openDB(DBPath(root))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var timeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout != busyTimeoutMillis {
		t.Fatalf("expected busy_timeout %d, got %d", busyTimeoutMillis, timeout)
	}
}

// TestRebuildCacheIsAtomic asserts a failed rebuild leaves the previous cache
// intact rather than a half-dropped one. RebuildCache replaces the whole cache
// (drop, recreate, replay), so it must commit as ONE transaction: a reader that
// lands mid-rebuild otherwise sees dropped/empty tables and answers "no rows"
// for an issue that plainly exists (so-b08's transient empty scan).
func TestRebuildCacheIsAtomic(t *testing.T) {
	root := seedProject(t, "pb-atomic")
	// Replace the log with one that drops the cached issue and cannot replay
	// (an orphan comment), as an out-of-band write such as a git checkout can.
	// A non-atomic rebuild drops the tables, fails mid-replay, and leaves the
	// cache destroyed; an atomic one rolls the whole replacement back.
	orphan := Event{
		Type:      EventTypeComment,
		IssueID:   "pb-missing",
		Timestamp: "2026-01-01T00:02:00Z",
		Payload:   map[string]string{"body": "orphan"},
	}
	line, err := json.Marshal(orphan)
	if err != nil {
		t.Fatalf("marshal orphan: %v", err)
	}
	if err := os.WriteFile(EventsPath(root), append(line, '\n'), 0600); err != nil {
		t.Fatalf("replace events log: %v", err)
	}
	err = RebuildCache(root)
	if err == nil {
		t.Fatal("expected rebuild to fail on an unappliable event")
	}
	if strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("unexpected busy error: %v", err)
	}
	// The pre-existing cache must survive the failed rebuild whole.
	db, err := openDB(DBPath(root))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	issue, err := getIssueByID(db, "pb-atomic")
	if err != nil {
		t.Fatalf("query issue after failed rebuild: %v", err)
	}
	if issue.Title != "Seed" {
		t.Fatalf("expected the prior cache intact, got %+v", issue)
	}
}
