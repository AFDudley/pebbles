package pebbles

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// HasParentChildSuffix reports whether childID uses the parentID.<N> suffix format.
func HasParentChildSuffix(parentID, childID string) bool {
	prefix := parentID + "."
	if !strings.HasPrefix(childID, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(childID, prefix)
	if suffix == "" {
		return false
	}
	_, err := strconv.Atoi(suffix)
	return err == nil
}

// NextChildIssueID previews the next available child ID for a parent issue,
// against the on-disk cache as of the last EnsureCache. It is a preview, not
// an allocation: nothing stops a concurrent writer from consuming the same
// suffix before this ID is used. Callers that go on to append an event
// consuming the returned ID (a rename onto it) must instead use
// AllocateChildAndAppend, which makes the allocation and that append one
// atomic step (so-a9f).
func NextChildIssueID(root, parentID string) (string, error) {
	if err := EnsureCache(root); err != nil {
		return "", err
	}
	db, err := openDB(DBPath(root))
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	// Resolve and validate the parent issue ID before scanning children.
	resolvedParent, err := resolveIssueID(db, parentID)
	if err != nil {
		return "", err
	}
	if err := ensureIssueExists(db, resolvedParent); err != nil {
		return "", err
	}
	return nextFreeChildSuffix(db, resolvedParent)
}

// nextFreeChildSuffix picks the smallest "parentID.N" suffix not already used
// by a direct child and not otherwise taken by any issue, per db's current
// content. The caller is responsible for db reflecting whatever snapshot of
// the events log the choice must be correct against.
func nextFreeChildSuffix(db *sql.DB, parentID string) (string, error) {
	usedSuffixes, err := loadChildSuffixes(db, parentID)
	if err != nil {
		return "", err
	}
	// Pick the smallest available suffix that isn't already in use.
	for suffix := 1; ; suffix++ {
		if usedSuffixes[suffix] {
			continue
		}
		candidate := fmt.Sprintf("%s.%d", parentID, suffix)
		available, err := issueIDAvailable(db, candidate)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
		usedSuffixes[suffix] = true
	}
}

// AllocateChildAndAppend atomically allocates the next free child ID under
// parentID and appends the events buildEvents returns for it — allocation
// and the consuming append happen as ONE critical section under the events-
// file lock (events_lock.go), so no concurrent `pb dep add --type
// parent-child` can ever observe the same free suffix (so-a9f: previously
// NextChildIssueID read a cache that lagged the very appends racing it,
// letting two callers compute and consume the same suffix — reproduced in
// production as two renames both landing on exo-72d.9).
//
// buildEvents is called with the allocated child ID only after allocation
// succeeds; an error from it rejects the whole call with the events log left
// completely untouched — the collision/validity check always precedes any
// append, so a rejected call never needs manual event-log surgery.
func AllocateChildAndAppend(root, parentID string, buildEvents func(childID string) ([]Event, error)) (string, error) {
	eventsPath := EventsPath(root)
	var childID string
	err := withEventsFileLock(eventsPath, func() error {
		// Holding the events-file lock excludes every other reader and
		// writer of the log (readEventsFile/AppendEvent take the same
		// lock), so a plain unlocked read here is exactly as current as any
		// lock-protected read could be.
		data, err := os.ReadFile(eventsPath)
		if err != nil {
			return fmt.Errorf("read events log: %w", err)
		}
		db, err := openDB(DBPath(root))
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		// Bring the cache to exactly this locked snapshot before allocating
		// from it. Do NOT also take withRebuildLock here: ensureCacheOnce's
		// slow path acquires rebuildLock THEN eventsLock (readEventsFile is
		// called inside its withRebuildLock closure), so acquiring them in
		// the opposite order here — eventsLock (already held) then
		// rebuildLock — is a lock-order inversion that deadlocks against a
		// concurrent ensureCacheOnce rebuild (reproduced: 3 of 8 concurrent
		// `pb dep add` calls completed, the rest hung forever). The
		// events-file lock already excludes every other reader and writer of
		// the log, including every other rebuild, so no second lock is
		// needed for exclusivity here.
		if err := rebuildCacheTx(db, data); err != nil {
			return err
		}
		resolvedParent, err := resolveIssueID(db, parentID)
		if err != nil {
			return err
		}
		if err := ensureIssueExists(db, resolvedParent); err != nil {
			return err
		}
		candidate, err := nextFreeChildSuffix(db, resolvedParent)
		if err != nil {
			return err
		}
		events, err := buildEvents(candidate)
		if err != nil {
			return err
		}
		if err := appendEventsLocked(eventsPath, events); err != nil {
			return err
		}
		childID = candidate
		return nil
	})
	if err != nil {
		return "", err
	}
	return childID, nil
}

// loadChildSuffixes collects numeric suffixes already used by direct children.
func loadChildSuffixes(db *sql.DB, parentID string) (map[int]bool, error) {
	rows, err := db.Query(
		"SELECT issue_id FROM deps WHERE dep_type = ? AND depends_on_id = ? ORDER BY issue_id",
		DepTypeParentChild,
		parentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list child deps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	used := make(map[int]bool)
	prefix := parentID + "."
	// Track suffixes already assigned to this parent's direct children.
	for rows.Next() {
		var childID string
		if err := rows.Scan(&childID); err != nil {
			return nil, fmt.Errorf("scan child dep: %w", err)
		}
		if !strings.HasPrefix(childID, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(childID, prefix)
		value, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		if value > 0 {
			used[value] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("child deps rows: %w", err)
	}
	return used, nil
}

// issueIDAvailable reports whether an ID is unused and not aliased by a rename.
func issueIDAvailable(db *sql.DB, id string) (bool, error) {
	resolved, err := resolveIssueID(db, id)
	if err != nil {
		return false, err
	}
	if resolved != id {
		return false, nil
	}
	exists, err := issueExists(db, id)
	if err != nil {
		return false, err
	}
	return !exists, nil
}
