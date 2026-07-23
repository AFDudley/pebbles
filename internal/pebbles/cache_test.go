package pebbles

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestSortEventsOrdersRenameBeforeDeps(t *testing.T) {
	timestamp := "2026-01-19T00:00:00Z"
	events := []Event{
		{Type: EventTypeStatus, Timestamp: timestamp},
		{Type: EventTypeDepAdd, Timestamp: timestamp},
		{Type: EventTypeRename, Timestamp: timestamp},
		{Type: EventTypeCreate, Timestamp: timestamp},
		{Type: EventTypeUpdate, Timestamp: timestamp},
		{Type: EventTypeClose, Timestamp: timestamp},
	}
	sortEvents(events)
	expected := []string{
		EventTypeCreate,
		EventTypeRename,
		EventTypeDepAdd,
		EventTypeStatus,
		EventTypeUpdate,
		EventTypeClose,
	}
	for i, eventType := range expected {
		if events[i].Type != eventType {
			t.Fatalf("expected %s at index %d, got %s", eventType, i, events[i].Type)
		}
	}
}

func TestEnsureCacheIgnoresDuplicateCreateEvents(t *testing.T) {
	root := t.TempDir()
	if err := InitProject(root); err != nil {
		t.Fatalf("init project: %v", err)
	}
	issueID := "pb-dupe"
	if err := AppendEvent(root, NewCreateEvent(issueID, "First", "", "task", "2024-01-01T00:00:00Z", 2)); err != nil {
		t.Fatalf("append create: %v", err)
	}
	if err := AppendEvent(root, NewCreateEvent(issueID, "First", "", "task", "2024-01-01T00:00:01Z", 2)); err != nil {
		t.Fatalf("append duplicate create: %v", err)
	}
	if err := EnsureCache(root); err != nil {
		t.Fatalf("rebuild cache: %v", err)
	}
	issues, err := ListIssues(root)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
}

// TestApplyEventsRenameResolvesByNewID guards the incremental replay invariant
// the pebble names: applying create+rename via applyEvents (not EnsureCache)
// must leave the NEW id resolvable to the row.
func TestApplyEventsRenameResolvesByNewID(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(dir + "/apply.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := ensureSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	events := []Event{
		NewCreateEvent("pb-old", "Renamed issue", "", "task", "2026-01-01T00:00:00Z", 2),
		NewRenameEvent("pb-old", "pb-new", "2026-01-01T00:00:01Z"),
	}
	if err := applyEvents(db, events); err != nil {
		t.Fatalf("apply events: %v", err)
	}
	resolved, err := resolveIssueID(db, "pb-new")
	if err != nil {
		t.Fatalf("resolve new id: %v", err)
	}
	exists, err := issueExists(db, resolved)
	if err != nil {
		t.Fatalf("issue exists: %v", err)
	}
	if !exists {
		t.Fatalf("expected new id pb-new to resolve to an existing row, got %q", resolved)
	}
}

// TestEnsureCacheDetectsOutOfBandRename reproduces so-fe0: the events log is
// mutated out of band (a rename appended by another process / git checkout /
// merge / PEBBLES_DIR shared across worktrees) while the cache db keeps a
// newer-or-equal mtime. mtime-based staleness detection misses it and serves a
// stale cache where the NEW id does not resolve ("sql: no rows in result set").
// A content-derived staleness check must rebuild and resolve the new id with no
// manual cache drop.
func TestEnsureCacheDetectsOutOfBandRename(t *testing.T) {
	root := t.TempDir()
	if err := InitProject(root); err != nil {
		t.Fatalf("init project: %v", err)
	}
	if err := AppendEvent(root, NewCreateEvent("pb-old", "Renamed issue", "", "task", "2026-01-01T00:00:00Z", 2)); err != nil {
		t.Fatalf("append create: %v", err)
	}
	// Build the cache so the db reflects the pre-rename state.
	if _, _, err := GetIssue(root, "pb-old"); err != nil {
		t.Fatalf("get issue before rename: %v", err)
	}
	// Out-of-band append of the rename event.
	if err := AppendEvent(root, NewRenameEvent("pb-old", "pb-new", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("append rename: %v", err)
	}
	// Simulate git checkout / merge / concurrent writer: the events log ends up
	// with an mtime NOT newer than the db (older here), defeating mtime checks.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(EventsPath(root), old, old); err != nil {
		t.Fatalf("chtimes events: %v", err)
	}
	issue, _, err := GetIssue(root, "pb-new")
	if err != nil {
		t.Fatalf("get issue by new id after out-of-band rename: %v", err)
	}
	if issue.ID != "pb-new" {
		t.Fatalf("expected resolved id pb-new, got %q", issue.ID)
	}
	// No stray rollback journal must survive a resolved read.
	if _, err := os.Stat(DBPath(root) + "-journal"); err == nil {
		t.Fatalf("stray pebbles.db-journal left behind")
	}
}

// TestOpenDBUsesWALMode guards exo-e32 clause c1: pb's cache connection must
// report journal_mode=wal, not SQLite's default rollback journal — the
// property that lets a reader avoid contending for the writer's file lock.
func TestOpenDBUsesWALMode(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(dir + "/wal.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("expected journal_mode wal, got %q", mode)
	}
}

// TestEnsureCacheRebuildsOnceOnCorruptCacheFile guards exo-e32 clauses
// c1's classification and c2: a pebbles.db that is not a readable SQLite
// file at all (garbage bytes — a torn write, a truncated copy, anything short
// of the log itself being bad) is CACHE INVALIDITY, not data loss. With a
// healthy events log, EnsureCache must recover by deleting the corrupt file
// and rebuilding from the log ONCE, transparently — the caller never sees an
// error and the rebuilt cache serves correct data.
func TestEnsureCacheRebuildsOnceOnCorruptCacheFile(t *testing.T) {
	root := t.TempDir()
	if err := InitProject(root); err != nil {
		t.Fatalf("init project: %v", err)
	}
	if err := AppendEvent(root, NewCreateEvent("pb-1", "Recoverable", "", "task", "2026-01-01T00:00:00Z", 2)); err != nil {
		t.Fatalf("append create: %v", err)
	}
	// Build a real cache once, then destroy it with garbage bytes — not a
	// missing file (a fresh-cache path), a PRESENT but unreadable one.
	if err := EnsureCache(root); err != nil {
		t.Fatalf("initial build: %v", err)
	}
	if err := os.WriteFile(DBPath(root), []byte("not a sqlite database, just garbage bytes"), 0o600); err != nil {
		t.Fatalf("corrupt cache file: %v", err)
	}
	if err := EnsureCache(root); err != nil {
		t.Fatalf("EnsureCache did not recover from a corrupt cache file: %v", err)
	}
	issues, err := ListIssues(root)
	if err != nil {
		t.Fatalf("list issues after recovery: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "pb-1" {
		t.Fatalf("expected the log's one issue after recovery, got %+v", issues)
	}
}

// TestEnsureCacheFailsLoudWhenLogAlsoCorrupt guards exo-e32 clause c3: when
// the corrupt-cache recovery's OWN rebuild attempt also fails — here because
// the events LOG itself is corrupt, so no rebuild can ever succeed —
// EnsureCache must return the failure from that second, final attempt rather
// than retry again or silently succeed. This is the fail-loud boundary: a
// bad cache is recoverable once from a good log, but a bad log is not
// recoverable at all.
func TestEnsureCacheFailsLoudWhenLogAlsoCorrupt(t *testing.T) {
	root := t.TempDir()
	if err := InitProject(root); err != nil {
		t.Fatalf("init project: %v", err)
	}
	if err := AppendEvent(root, NewCreateEvent("pb-1", "Not recoverable", "", "task", "2026-01-01T00:00:00Z", 2)); err != nil {
		t.Fatalf("append create: %v", err)
	}
	if err := EnsureCache(root); err != nil {
		t.Fatalf("initial build: %v", err)
	}
	if err := os.WriteFile(DBPath(root), []byte("garbage cache"), 0o600); err != nil {
		t.Fatalf("corrupt cache file: %v", err)
	}
	if err := os.WriteFile(EventsPath(root), []byte("this is not a valid events log\n"), 0o600); err != nil {
		t.Fatalf("corrupt events log: %v", err)
	}
	err := EnsureCache(root)
	if err == nil {
		t.Fatalf("expected EnsureCache to fail loud when the log itself is corrupt, got nil")
	}
}

// TestIsCacheCorruptionDistinguishesCacheFromLogErrors guards the
// classification EnsureCache's recovery branches on: a genuinely-unreadable
// SQLite cache file must be recognized (so it is deleted and retried), while
// an events-LOG problem (a malformed JSONL line) must NOT be, since deleting
// and rebuilding the cache cannot fix a bad log — the second attempt would
// fail identically, wasting the one retry EnsureCache grants.
func TestIsCacheCorruptionDistinguishesCacheFromLogErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sqlite notadb", errors.New("begin rebuild: file is not a database (26)"), true},
		{"sqlite corrupt", errors.New("load events hash: database disk image is malformed (11)"), true},
		{"log parse error", errors.New("parse event: unexpected end of JSON input"), false},
		{"log scan error", errors.New("scan events log: unexpected EOF"), false},
		{"log missing", errors.New("read events log: open events log: no such file or directory"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCacheCorruption(tc.err); got != tc.want {
				t.Fatalf("isCacheCorruption(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
