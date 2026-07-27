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

// TestFreshCacheReadDoesNotContendForWriteLock reproduces exo-e32's residual
// c5 defect: every EnsureCache call, including a pure reader's, unconditionally
// opened a BEGIN IMMEDIATE transaction just to ask "is the cache fresh?" —
// so a read contended for the SAME single exclusive lock as every concurrent
// writer's rebuild. Under sustained multi-process write load that starved
// some callers past busy_timeout ("begin rebuild: database is locked
// (SQLITE_BUSY)"). The fast path in ensureCacheOnce checks freshness via a
// plain read (WAL gives it its own snapshot, no lock needed) before ever
// calling db.Begin(). Proof here: hold the exclusive write lock open and
// uncommitted, then confirm a fresh-cache EnsureCache still returns promptly
// instead of queuing behind it.
func TestFreshCacheReadDoesNotContendForWriteLock(t *testing.T) {
	root := seedProject(t, "pb-freshread")
	// Hold the write lock indefinitely without committing or rolling back.
	holder, err := openDB(DBPath(root))
	if err != nil {
		t.Fatalf("open holder db: %v", err)
	}
	defer func() { _ = holder.Close() }()
	tx, err := holder.Begin()
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The cache is fresh (seedProject built it; nothing has changed since).
	done := make(chan error, 1)
	go func() { done <- EnsureCache(root) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureCache on a fresh cache failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureCache blocked on the held write lock despite a fresh cache — the fast path regressed to always taking the exclusive lock")
	}
}

// TestManyConcurrentWritersNeverProduceATornLogRead reproduces exo-e32's
// other residual defect: AppendEvent writes a JSONL line with a single
// write(2) call, which Linux does not guarantee atomic against a concurrent
// reader of a regular file — a read racing exactly against an in-flight
// append can observe a partial line and fail with "parse event: unexpected
// end of JSON input" (the exact stderr from the 2026-07-23 production crash,
// reproduced under 10 real OS processes by
// scripts/verify_pb_concurrent_read_write.py). withEventsFileLock
// (events_lock.go) closes that window for every log reader and writer; this
// in-process regression hammers LoadEvents against many concurrent AppendEvent
// callers.
func TestManyConcurrentWritersNeverProduceATornLogRead(t *testing.T) {
	const writers = 20
	const perWriter = 50
	root := seedProject(t, "pb-torn")
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				event := NewCommentEvent("pb-torn", fmt.Sprintf("w%d-i%d-%s", index, j, strings.Repeat("x", 40)), "2026-01-01T00:01:00Z")
				if err := AppendEvent(root, event); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(i)
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	reads := 0
	for {
		select {
		case <-done:
			if reads == 0 {
				t.Fatal("no reads raced the writers")
			}
			return
		default:
		}
		if _, err := LoadEvents(root); err != nil {
			t.Fatalf("read %d during concurrent writes failed: %v", reads, err)
		}
		reads++
	}
}

// TestConcurrentAllocateChildAndAppendNeverDuplicates reproduces pebble
// so-a9f's incident: NextChildIssueID picked a suffix from the cache and the
// caller appended the consuming rename separately, so two overlapping
// `pb dep add --type parent-child` calls could both compute and consume the
// same suffix (production: two renames both landing on exo-72d.9, then every
// cache rebuild aborting with "issue already exists"). AllocateChildAndAppend
// makes the allocation and its consuming append one critical section under
// the events-file lock, so N concurrent callers linking under the same
// parent must produce N distinct, gapless suffixes and all succeed — never a
// duplicate, never a partial write.
func TestConcurrentAllocateChildAndAppendNeverDuplicates(t *testing.T) {
	const linkers = 8
	root := seedProject(t, "pb-parent")
	var wg sync.WaitGroup
	childIDs := make([]string, linkers)
	errs := make([]error, linkers)
	start := make(chan struct{})
	for i := 0; i < linkers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			childID, err := AllocateChildAndAppend(root, "pb-parent", func(candidate string) ([]Event, error) {
				ts := "2026-01-01T00:01:00Z"
				return []Event{
					NewCreateEvent(candidate, fmt.Sprintf("Child %d", index), "", "task", ts, 2),
					NewDepAddEvent(candidate, "pb-parent", DepTypeParentChild, ts),
				}, nil
			})
			childIDs[index] = childID
			errs[index] = err
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[string]bool, linkers)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("linker %d failed: %v", i, err)
		}
		if seen[childIDs[i]] {
			t.Fatalf("duplicate child id assigned: %s", childIDs[i])
		}
		seen[childIDs[i]] = true
	}
	if len(seen) != linkers {
		t.Fatalf("expected %d distinct child ids, got %d", linkers, len(seen))
	}
	for suffix := 1; suffix <= linkers; suffix++ {
		want := fmt.Sprintf("pb-parent.%d", suffix)
		if !seen[want] {
			t.Fatalf("expected contiguous suffix %s to have been assigned, got %v", want, childIDs)
		}
	}

	// The tracker must remain fully readable after the burst: a cache
	// rebuilt from scratch must apply the whole log without aborting.
	if err := removeCacheFiles(DBPath(root)); err != nil {
		t.Fatalf("remove cache: %v", err)
	}
	if err := EnsureCache(root); err != nil {
		t.Fatalf("rebuild cache after burst: %v", err)
	}
}

// TestRejectedAllocateChildAndAppendWritesNothing asserts buildEvents'
// rejection is honored before any append: the collision/validity check must
// precede any write so a rejected call never leaves the log in a state that
// needs manual surgery (so-a9f).
func TestRejectedAllocateChildAndAppendWritesNothing(t *testing.T) {
	root := seedProject(t, "pb-reject")
	before, err := os.ReadFile(EventsPath(root))
	if err != nil {
		t.Fatalf("read events log: %v", err)
	}
	rejectErr := fmt.Errorf("rejected by test")
	_, err = AllocateChildAndAppend(root, "pb-reject", func(candidate string) ([]Event, error) {
		return nil, rejectErr
	})
	if err == nil {
		t.Fatal("expected AllocateChildAndAppend to fail")
	}
	after, err := os.ReadFile(EventsPath(root))
	if err != nil {
		t.Fatalf("read events log: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("rejected call wrote to the events log: before=%q after=%q", before, after)
	}
}

// TestEventsFileLockExcludesConcurrentAccess asserts withEventsFileLock is a
// real mutual-exclusion primitive: a second acquisition must block until the
// first releases, never run concurrently with it.
func TestEventsFileLockExcludesConcurrentAccess(t *testing.T) {
	root := seedProject(t, "pb-lock-events")
	path := EventsPath(root)
	locked := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = withEventsFileLock(path, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	acquired := make(chan struct{})
	go func() {
		_ = withEventsFileLock(path, func() error {
			close(acquired)
			return nil
		})
	}()
	select {
	case <-acquired:
		t.Fatal("second lock acquired while the first was still held")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired after the first released")
	}
}
