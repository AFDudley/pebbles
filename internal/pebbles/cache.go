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

// busyTimeoutMillis is how long SQLite waits for a held lock before erroring.
const busyTimeoutMillis = 5000

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
//
// The whole replacement — drop, recreate, replay, digest — commits as ONE
// transaction. It replaces every row in the cache, so a reader that observed it
// midway would see dropped or half-populated tables and answer "no rows" for an
// issue that plainly exists (pebble so-b08's transient empty scan). Inside a
// transaction there is no midway: a concurrent reader sees either the whole
// previous cache or the whole new one, and a rebuild that fails rolls back
// rather than leaving the cache destroyed.
func RebuildCache(root string) error {
	// Derive the events and their digest from a SINGLE read of the log, so the
	// digest describes exactly what was replayed. Reading the log twice lets a
	// concurrent append land in between: the cache would then record a digest
	// covering an event it never applied, and needsRebuild — which compares
	// that digest against the log — would call the cache fresh and serve the
	// missing event to nobody until the next write.
	data, err := os.ReadFile(EventsPath(root))
	if err != nil {
		return fmt.Errorf("read events log: %w", err)
	}
	events, err := decodeEvents(data)
	if err != nil {
		return err
	}
	hash := hashEvents(data)
	// Normalize event order before replay.
	sortEvents(events)
	db, err := openDB(DBPath(root))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin rebuild: %w", err)
	}
	// Roll back unless the commit below succeeds; a rolled-back rebuild leaves
	// the previous cache intact and its digest unchanged, so the next open
	// simply rebuilds again.
	defer func() { _ = tx.Rollback() }()
	// Recreate schema and replay the event log.
	if err := resetSchema(tx); err != nil {
		return err
	}
	if err := ensureSchema(tx); err != nil {
		return err
	}
	if err := applyEvents(tx, events); err != nil {
		return err
	}
	// Record the digest of the events log this cache was built from.
	if err := storeEventsHash(tx, hash); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rebuild: %w", err)
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
	return hashEvents(data), nil
}

// hashEvents returns a hex digest of raw events log content.
func hashEvents(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// storeEventsHash records the events-log digest for the built cache.
func storeEventsHash(db sqlExecutor, hash string) error {
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
func loadEventsHash(db sqlExecutor) (string, error) {
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

// openDB opens the SQLite cache at the given path.
//
// This is the ONE place pb opens a connection, so every verb — read and write
// alike — inherits this DSN (pebble so-b08):
//
//   - _pragma=busy_timeout(N) makes a connection WAIT up to N ms for a lock
//     another process holds instead of failing immediately with
//     "database is locked (SQLITE_BUSY)". SQLite's own default is 0 (error at
//     once), which is wrong for pb: several agents routinely share one bus, so
//     a contended write is normal, not exceptional. The driver executes the
//     pragma on each new connection at open (modernc.org/sqlite conn.go
//     newConn -> applyQueryParams), and orders busy_timeout first.
//     This is not a retry loop: SQLite's busy handler waits on the lock and the
//     operation still fails loudly if the lock is not released within N ms.
//
//   - _txlock=immediate makes BeginTx issue BEGIN IMMEDIATE, taking the write
//     lock up front. A deferred transaction that reads first and writes later
//     can fail SQLITE_BUSY on the upgrade even with a busy_timeout set, because
//     SQLite cannot safely wait once another writer has changed the database
//     underneath the reader's snapshot.
func openDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_txlock=immediate", path, busyTimeoutMillis)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return db, nil
}

// sqlExecutor is the subset of *sql.DB the replay helpers need, so the same
// code applies events to either a database handle or a transaction.
type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
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
