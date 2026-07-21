package pebbles

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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
	if err := EnsureCache(root); err != nil {
		t.Fatalf("rebuild cache: %v", err)
	}
	return root
}

// TestConcurrentWritersAllSucceed reproduces pebble so-b08's write half: N
// concurrent writers each append an event and refresh the cache, exactly as a
// write verb does (cmd/pb/main.go: AppendEvent then EnsureCache). Without an
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
			if err := EnsureCache(root); err != nil {
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
			if err := EnsureCache(root); err != nil {
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

// TestEnsureCacheIsAtomic asserts a failed rebuild leaves the previous cache
// intact rather than a half-dropped one. EnsureCache replaces the whole cache
// (drop, recreate, replay), so it must commit as ONE transaction: a reader that
// lands mid-rebuild otherwise sees dropped/empty tables and answers "no rows"
// for an issue that plainly exists (so-b08's transient empty scan).
func TestEnsureCacheIsAtomic(t *testing.T) {
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
	err = EnsureCache(root)
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

// TestEnsureCacheChecksStalenessUnderTheWriteLock reproduces pebble so-391's
// residual race: so-b08 made the rebuild itself one immediate transaction, but
// the STALENESS CHECK still ran before the write lock was taken. Every process
// that observed a stale hash then performed its own full rebuild once the lock
// freed, so one hash change under contention convoyed N redundant full
// rebuilds — and the busy_timeout budget expired mid-convoy in production
// ("reset schema: database is locked (5) (SQLITE_BUSY)").
//
// The discriminator: while one handle holds the write lock, it rebuilds the
// cache to the CURRENT log hash and plants a marker row in cache_meta. A
// concurrent EnsureCache that queued behind it must re-check staleness UNDER
// the lock, see the cache fresh, and touch nothing — the marker survives. The
// pre-lock-check code instead decided "stale" up front and, after waiting,
// dropped and rebuilt every table redundantly — destroying the marker.
func TestEnsureCacheChecksStalenessUnderTheWriteLock(t *testing.T) {
	root := seedProject(t, "pb-lock")
	// Stale the cache: append an event so the log hash no longer matches.
	event := NewCommentEvent("pb-lock", "stale it", "2026-01-01T00:01:00Z")
	if err := AppendEvent(root, event); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Handle A takes the write lock (openDB's _txlock makes Begin immediate).
	dbA, err := openDB(DBPath(root))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = dbA.Close() }()
	txA, err := dbA.Begin()
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	// The contender queues behind handle A's write lock.
	errCh := make(chan error, 1)
	go func() { errCh <- EnsureCache(root) }()
	// Give the contender time to run any pre-lock staleness check and block on
	// the lock. The green path does not depend on this window — a correct
	// EnsureCache re-checks under the lock no matter when it acquires it.
	time.Sleep(500 * time.Millisecond)
	// While holding the lock: rebuild to the current log hash and plant the
	// marker, exactly what a winning contender does.
	data, err := os.ReadFile(EventsPath(root))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	events, err := decodeEvents(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sortEvents(events)
	if err := resetSchema(txA); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := ensureSchema(txA); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := applyEvents(txA, events); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := storeEventsHash(txA, hashEvents(data)); err != nil {
		t.Fatalf("store hash: %v", err)
	}
	if _, err := txA.Exec(
		"INSERT INTO cache_meta (key, value) VALUES ('so391-marker', '1')",
	); err != nil {
		t.Fatalf("plant marker: %v", err)
	}
	if err := txA.Commit(); err != nil {
		t.Fatalf("commit holder tx: %v", err)
	}
	// The contender must WAIT and succeed — never SQLITE_BUSY.
	if err := <-errCh; err != nil {
		t.Fatalf("concurrent EnsureCache failed: %v", err)
	}
	// The cache was fresh when the contender got the lock, so it must not have
	// rebuilt: the marker survives. A destroyed marker is the redundant
	// rebuild — the pre-lock staleness decision so-391 removes.
	dbB, err := openDB(DBPath(root))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = dbB.Close() }()
	var v string
	err = dbB.QueryRow(
		"SELECT value FROM cache_meta WHERE key = 'so391-marker'",
	).Scan(&v)
	if err != nil {
		t.Fatalf("marker destroyed: the contender rebuilt a fresh cache (staleness was decided before the lock): %v", err)
	}
}

// TestConcurrentRebuildRacersAllSucceed is the so-b08 concurrency suite
// extended to so-391's trigger: a rebuild-triggering hash change with N
// concurrent read verbs racing to rebuild. Every racer must WAIT and succeed;
// under the pre-lock staleness check they convoy N full rebuilds instead,
// which under production contention blows the busy_timeout budget.
func TestConcurrentRebuildRacersAllSucceed(t *testing.T) {
	const racers = 8
	root := seedProject(t, "pb-race")
	// One hash change stales the cache for every racer at once.
	event := NewCommentEvent("pb-race", "hash change", "2026-01-01T00:01:00Z")
	if err := AppendEvent(root, event); err != nil {
		t.Fatalf("append: %v", err)
	}
	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = EnsureCache(root)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d failed: %v", i, err)
		}
	}
	// The cache must be fresh after the race: the appended comment is visible.
	comments, err := ListIssueComments(root, "pb-race")
	if err != nil {
		t.Fatalf("get comments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}
