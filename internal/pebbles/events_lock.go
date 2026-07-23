package pebbles

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// withFlock runs fn while holding an exclusive OS advisory lock on lockPath
// (created if absent), blocking with NO timeout until it is free.
//
// flock is a real kernel wait queue, not a polling retry loop: a caller waits
// only as long as the current holder's critical section actually takes, with
// no numeric budget to exceed. That distinction is exactly why this replaces
// SQLite's busy_timeout as the serialization point for a cache rebuild
// (ensureCacheOnce): busy_timeout is an inherently bounded wait (5000ms), and
// any workload that can queue more aggregate contention than that budget
// eventually exceeds it regardless of the number chosen — reproduced under
// exo-e32's c5 stress oracle at 200 writer iterations as "begin rebuild:
// database is locked (SQLITE_BUSY)". Serializing rebuild attempts across
// processes via flock first means at most one process is ever inside
// db.Begin()..Commit() for a rebuild at a time, so SQLite's own internal lock
// never has more than one real contender to wait out.
func withFlock(lockPath string, fn func() error) error {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("acquire lock %s: %w", lockPath, err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	return fn()
}

// withEventsFileLock serializes access to the events log file at eventsPath
// (a read of its full content, or a single append) across processes, via an
// OS advisory lock on a sibling ".lock" file.
//
// AppendEvent writes one line with a single write(2) call, which Linux does
// NOT guarantee atomic against a concurrent reader of a regular file: the
// buffered-write path excludes other writers but not readers, so a reader
// racing exactly against an in-flight append can observe a partial line and
// fail to parse it ("parse event: unexpected end of JSON input" — the exact
// defect exo-e32 reproduced under ten concurrent writer processes, distinct
// from the pebbles.db SQLite cache's own concurrency, which openDB's WAL mode
// already covers). flock(LOCK_EX) around every append AND every full-log read
// closes that window: a read can only ever observe the log wholly before or
// wholly after a given append, never mid-flight. Every critical section here
// is one small file read or one small append, so a caller waits only as long
// as real work takes — this is a lock, not a retry loop.
func withEventsFileLock(eventsPath string, fn func() error) error {
	return withFlock(eventsPath+".lock", fn)
}

// withRebuildLock serializes cache-rebuild attempts (ensureCacheOnce's slow
// path) at dbPath across processes — see withFlock's doc comment for why this
// replaces relying on SQLite's own busy_timeout for cross-process exclusion.
func withRebuildLock(dbPath string, fn func() error) error {
	return withFlock(dbPath+".rebuild.lock", fn)
}
