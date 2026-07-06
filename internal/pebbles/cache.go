package pebbles

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// cacheMetaEventsHash is the cache_meta key recording the digest of the events
// log the cache was last built from. See needsRebuild.
const cacheMetaEventsHash = "events_hash"

// EnsureCache rebuilds the cache when it no longer matches the events log.
func EnsureCache(root string) error {
	needs, err := needsRebuild(EventsPath(root), DBPath(root))
	if err != nil {
		return err
	}
	if needs {
		return RebuildCache(root)
	}
	return nil
}

// RebuildCache recreates the SQLite cache from the event log.
func RebuildCache(root string) error {
	events, err := LoadEvents(root)
	if err != nil {
		return err
	}
	// Digest the log up front so the cache records exactly what it replayed.
	hash, err := hashEventsFile(EventsPath(root))
	if err != nil {
		return err
	}
	// Normalize event order before replay.
	sortEvents(events)
	db, err := openDB(DBPath(root))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	// Recreate schema and replay the event log.
	if err := resetSchema(db); err != nil {
		return err
	}
	if err := ensureSchema(db); err != nil {
		return err
	}
	if err := applyEvents(db, events); err != nil {
		return err
	}
	// Record the digest of the events log this cache was built from. Written
	// last so a partially-built (e.g. crash/rollback) cache lacks the marker
	// and is treated as stale on the next open.
	if err := storeEventsHash(db, hash); err != nil {
		return err
	}
	return nil
}

// needsRebuild reports whether the cache no longer matches the events log.
//
// Staleness is derived from CONTENT, not file mtime: the cache stores the
// digest of the events log it was built from, and a rebuild is required when
// that digest is absent (missing/old-schema/partial cache) or differs from the
// current log's digest. mtime is unreliable — an out-of-band write (git
// checkout/merge, a concurrent writer, or a PEBBLES_DIR shared across
// worktrees) can leave the log's mtime not-newer than the db while its content
// diverges, which a timestamp comparison silently misses (pebble so-fe0).
func needsRebuild(eventsPath, dbPath string) (bool, error) {
	if _, err := os.Stat(dbPath); err != nil {
		// Missing cache means a rebuild is required.
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("stat cache: %w", err)
	}
	wantHash, err := hashEventsFile(eventsPath)
	if err != nil {
		return false, err
	}
	db, err := openDB(dbPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	gotHash, err := loadEventsHash(db)
	if err != nil {
		return false, err
	}
	return gotHash != wantHash, nil
}

// hashEventsFile returns a hex digest of the events log content.
func hashEventsFile(eventsPath string) (string, error) {
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return "", fmt.Errorf("read events log: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// storeEventsHash records the events-log digest for the built cache.
func storeEventsHash(db *sql.DB, hash string) error {
	if _, err := db.Exec(
		"INSERT OR REPLACE INTO cache_meta (key, value) VALUES (?, ?)",
		cacheMetaEventsHash,
		hash,
	); err != nil {
		return fmt.Errorf("store events hash: %w", err)
	}
	return nil
}

// loadEventsHash reads the recorded events-log digest, or "" when absent.
func loadEventsHash(db *sql.DB) (string, error) {
	row := db.QueryRow("SELECT value FROM cache_meta WHERE key = ?", cacheMetaEventsHash)
	var hash string
	if err := row.Scan(&hash); err != nil {
		// No cache_meta row (or table) means an old/partial cache: rebuild.
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if isNoSuchTable(err) {
			return "", nil
		}
		return "", fmt.Errorf("load events hash: %w", err)
	}
	return hash, nil
}

// isNoSuchTable reports whether err is SQLite's missing-table error, which an
// old cache built before the cache_meta table existed will produce.
func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// openDB opens a SQLite database at the given path.
func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return db, nil
}

// sortEvents orders events by timestamp with a stable fallback.
// eventTypePriority returns a sort order for event types.
// Creates must come before deps/comments, which must come before status changes.
func eventTypePriority(eventType string) int {
	switch eventType {
	case "create":
		return 0
	case "rename":
		return 1
	case "dep_add", "dep_rm", "comment":
		return 2
	default: // close, update, status, etc.
		return 3
	}
}
func sortEvents(events []Event) {
	// Preserve original ordering by embedding an index.
	type indexed struct {
		Event
		Index int
	}
	indexedEvents := make([]indexed, 0, len(events))
	for i, event := range events {
		indexedEvents = append(indexedEvents, indexed{Event: event, Index: i})
	}
	// Sort by event type priority, then timestamp, then original index.
	// This ensures creates come before deps, which come before status changes.
	sort.SliceStable(indexedEvents, func(i, j int) bool {
		priI := eventTypePriority(indexedEvents[i].Type)
		priJ := eventTypePriority(indexedEvents[j].Type)
		if priI != priJ {
			return priI < priJ
		}
		timeI, errI := time.Parse(time.RFC3339Nano, indexedEvents[i].Timestamp)
		timeJ, errJ := time.Parse(time.RFC3339Nano, indexedEvents[j].Timestamp)
		if errI == nil && errJ == nil && !timeI.Equal(timeJ) {
			return timeI.Before(timeJ)
		}
		return indexedEvents[i].Index < indexedEvents[j].Index
	})
	for i := range indexedEvents {
		events[i] = indexedEvents[i].Event
	}
}
