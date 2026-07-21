package pebbles

import (
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
