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
// log the cache was last built from. See EnsureCache.
const cacheMetaEventsHash = "events_hash"

// busyTimeoutMillis is how long SQLite waits for a held lock before erroring.
const busyTimeoutMillis = 5000

// EnsureCache makes the SQLite cache match the events log, rebuilding it when
// it no longer does.
//
// The staleness check and the rebuild run as ONE immediate transaction
// (pebble so-391). so-b08 made the rebuild itself a single transaction, but
// the staleness check still ran BEFORE the write lock was taken: every
// process that observed a stale hash then performed its own full rebuild once
// the lock freed, so one hash change under contention convoyed N redundant
// full rebuilds — and the busy_timeout budget expired mid-convoy ("reset
// schema: database is locked (5) (SQLITE_BUSY)" in production). With the
// check inside the transaction, the first contender rebuilds and every queued
// contender re-reads the hash under the lock, sees the cache fresh, and does
// nothing. openDB's _txlock=immediate makes Begin take the write lock up
// front, so a contended rebuild WAITS on busy_timeout instead of failing on a
// read→write upgrade; past the budget it still fails loud.
//
// Staleness is derived from CONTENT, not file mtime: the cache stores the
// digest of the events log it was built from, and a rebuild is required when
// that digest is absent (missing/old-schema/partial cache) or differs from
// the current log's digest. mtime is unreliable — an out-of-band write (git
// checkout/merge, a concurrent writer, or a PEBBLES_DIR shared across
// worktrees) can leave the log's mtime not-newer than the db while its
// content diverges, which a timestamp comparison silently misses (so-fe0).
//
// The whole replacement — check, drop, recreate, replay, digest — commits as
// ONE transaction. It replaces every row in the cache, so a reader that
// observed it midway would see dropped or half-populated tables and answer
// "no rows" for an issue that plainly exists (so-b08's transient empty scan).
// Inside a transaction there is no midway: a concurrent reader sees either
// the whole previous cache or the whole new one, and a rebuild that fails
// rolls back rather than leaving the cache destroyed.
//
// A cache database that is itself unreadable — not merely stale, but garbage
// (truncated, overwritten, a non-SQLite file) — is CACHE INVALIDITY, not data
// loss: it is rebuilt from events.jsonl exactly like a stale one (exo-e32).
// EnsureCache delegates the check-and-rebuild to ensureCacheOnce and, if that
// fails with a signature identifying the CACHE FILE itself as unreadable
// (isCacheCorruption), deletes the corrupt cache and its WAL/journal
// siblings and rebuilds ONCE more from the log. A second consecutive failure
// — whether the rebuilt cache is corrupt again or the events log itself is
// unparseable — is fail-loud corruption of the LOG: it returns the error
// from that second attempt with no further retry.
func EnsureCache(root string) error {
	if err := ensureCacheOnce(root); err != nil {
		if !isCacheCorruption(err) {
			return err
		}
		if rmErr := removeCacheFiles(DBPath(root)); rmErr != nil {
			return fmt.Errorf("remove corrupt cache (after %v): %w", err, rmErr)
		}
		if err := ensureCacheOnce(root); err != nil {
			return fmt.Errorf("rebuild after cache invalidation: %w", err)
		}
	}
	return nil
}

// removeCacheFiles deletes the SQLite cache file at path plus its WAL/
// shared-memory/rollback-journal siblings, so a rebuild starts from nothing
// rather than reusing any surviving corrupt fragment. Missing files are not
// an error — the cache may never have grown a WAL/journal file at all.
func removeCacheFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s%s: %w", path, suffix, err)
		}
	}
	return nil
}

// isCacheCorruption reports whether err names the SQLite CACHE FILE itself as
// unreadable — SQLITE_NOTADB ("file is not a database") or SQLITE_CORRUPT
// ("database disk image is malformed"), both signatures a genuinely-garbage
// pebbles.db produces before EnsureCache ever reaches the events log. Distinct
// from an events-log problem (decodeEvents' "parse event:" / "scan events
// log:", or a missing log from "read events log:"): those occur only AFTER
// the cache file itself opened and began a transaction successfully, so a
// fresh cache would fail identically — no point deleting and retrying, and
// EnsureCache does not try (see it, above).
func isCacheCorruption(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "file is not a database") ||
		strings.Contains(msg, "database disk image is malformed")
}

// ensureCacheOnce is EnsureCache's single check-and-rebuild attempt (see its
// doc comment for the staleness/atomicity discipline); EnsureCache wraps it
// with the exo-e32 rebuild-once-on-corruption recovery.
func ensureCacheOnce(root string) error {
	db, err := openDB(DBPath(root))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Fast path: a plain read of the cache's recorded digest, with no
	// exclusive lock taken. WAL mode (openDB) gives this its own consistent
	// snapshot with zero contention against a concurrent writer's BEGIN
	// IMMEDIATE below -- that MVCC isolation is the whole point of WAL. Every
	// caller of EnsureCache, including a pure reader like `pb ready`,
	// previously had to win the SAME single exclusive lock as every
	// concurrent writer's rebuild just to ask "is the cache fresh?" -- under
	// sustained multi-process write load that starved some callers past
	// busy_timeout ("begin rebuild: database is locked (SQLITE_BUSY)",
	// reproduced under exo-e32's c5 ten-writer stress oracle). A mismatch
	// here does not rebuild directly -- it falls through and is re-checked
	// inside the transaction below, preserving so-391's guarantee that only
	// genuine staleness escalates to the lock, and only the first contender
	// past it actually rebuilds.
	data, err := readEventsFile(EventsPath(root))
	if err != nil {
		return fmt.Errorf("read events log: %w", err)
	}
	if got, err := loadEventsHash(db); err != nil {
		return err
	} else if got == hashEvents(data) {
		return nil
	}

	// Slow path: the cache looks stale. Serialize the rebuild across
	// PROCESSES via flock before ever calling db.Begin() — see
	// withRebuildLock's doc comment for why this, not busy_timeout, is the
	// cross-process exclusion point. Everything from here on runs with at
	// most one contender past the flock at a time, so BEGIN IMMEDIATE below
	// contends only with a genuinely slow holder, never a queue of peers.
	return withRebuildLock(DBPath(root), func() error {
		// Read the log INSIDE the write lock, and derive the events and their
		// digest from that SINGLE read. Inside the lock: the freshness
		// decision and the replay then describe a log snapshot no older than
		// any rebuild this transaction queued behind, so a queued rebuild can
		// never overwrite a newer cache from an older snapshot. A single
		// read: reading the log twice lets a concurrent append land in
		// between, recording a digest that covers an event the replay never
		// applied (so-b08).
		data, err := readEventsFile(EventsPath(root))
		if err != nil {
			return fmt.Errorf("read events log: %w", err)
		}
		return rebuildCacheTx(db, data)
	})
}

// rebuildCacheTx replays data into db as ONE transaction if its digest
// differs from the cache's recorded one; a matching digest is a no-op that
// touches nothing. It performs no locking and no I/O of its own beyond the
// transaction — the caller supplies data already read under whatever lock
// makes that read authoritative (ensureCacheOnce's withRebuildLock hold, or
// AllocateChildAndAppend's events-file lock hold, which excludes every
// writer AND reader and so needs no separate rebuild lock to be correct).
func rebuildCacheTx(db *sql.DB, data []byte) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin rebuild: %w", err)
	}
	// Roll back unless the commit below succeeds; a rolled-back rebuild
	// leaves the previous cache intact and its digest unchanged, so the
	// next open simply rebuilds again. On the fresh path the rollback
	// just ends a transaction that wrote nothing.
	defer func() { _ = tx.Rollback() }()
	hash := hashEvents(data)
	got, err := loadEventsHash(tx)
	if err != nil {
		return err
	}
	if got == hash {
		// Fresh — either it was never stale, or a contender this flock
		// held behind it already rebuilt it. Touch nothing.
		return nil
	}
	events, err := decodeEvents(data)
	if err != nil {
		return err
	}
	// Normalize event order before replay.
	sortEvents(events)
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
	if err := storeEventsHash(tx, hash); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rebuild: %w", err)
	}
	return nil
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
// alike — inherits this DSN (pebble so-b08, WAL mode added exo-e32):
//
//   - _pragma=journal_mode(wal) puts the cache in write-ahead-log mode instead
//     of SQLite's default rollback journal. Under the rollback journal a
//     reader and a writer still contend for the SAME file lock, so even with
//     busy_timeout set a reader can be made to wait behind a writer for the
//     whole budget; WAL gives readers a private, consistent snapshot of the
//     database as of when they started, so a concurrent writer never blocks
//     or is blocked by a read — the exact "readers never observe mid-write
//     state" property pebble exo-e32 requires. journal_mode=WAL is persisted
//     in the database file's header once set, so it survives across
//     connections/processes; only ever needs setting once per file.
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
//     WAL mode does not eliminate writer-writer contention (two processes both
//     wanting the single write lock), so busy_timeout still matters there.
//
//   - _txlock=immediate makes BeginTx issue BEGIN IMMEDIATE, taking the write
//     lock up front. A deferred transaction that reads first and writes later
//     can fail SQLITE_BUSY on the upgrade even with a busy_timeout set, because
//     SQLite cannot safely wait once another writer has changed the database
//     underneath the reader's snapshot.
func openDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(%d)&_txlock=immediate",
		path, busyTimeoutMillis,
	)
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
