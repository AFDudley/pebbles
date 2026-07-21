# Changelog

All notable changes to Pebbles will be documented in this file.

The format is based on Keep a Changelog, and this project follows SemVer.

## [Unreleased]

### Added


### Changed


### Fixed
- Concurrent access no longer errors with `database is locked (SQLITE_BUSY)`.
  SQLite is opened with a 5s `busy_timeout`, so a contended read or write waits
  for the lock instead of failing immediately (so-b08).
- `pb show` and other reads during an in-flight write no longer return a
  transient `sql: no rows in result set`. The cache rebuild now commits as one
  transaction, so a reader never observes it mid-replay (so-b08).
- Concurrent cache rebuilds no longer convoy into `database is locked
  (SQLITE_BUSY)`. The staleness check now runs INSIDE the same immediate
  transaction as the rebuild (`EnsureCache`; `RebuildCache` is folded into
  it), so a contender that waited out another process's rebuild re-checks the
  events-log digest under the lock and skips instead of redundantly dropping
  and replaying the whole cache (so-391).

## [0.4.0] - 2026-01-21

### Added
- `pb list`, `pb show`, and `pb ready` now support `--json` output.
- `pb reopen` command to reopen closed issues.
- `pb list --blocked` to surface issues blocked by open dependencies.
- `pb list --stale`/`--stale-days` to show inactive open issues with last activity dates.
- `pb self-update` command to install the latest release.
- Markdown rendering for description/body fields in `pb log` output.

### Changed
- Refreshed `pb log` color palette for better contrast.
- Expanded CLI help text across commands.

### Fixed
- Adjusted `pb log` description formatting for consistent output.

## [0.3.0] - 2026-01-20

### Fixed
- Rebuild cache ordering now applies rename events before dependency/status replay.

## [0.2.0] - 2026-01-19

### Added
- pb update can now change type, priority, and description.
- Beads import workflow (spec + implementation).
- Colored output for `pb log`.


## [0.1.0] - 2026-01-19

### Added
- Initial public release.
