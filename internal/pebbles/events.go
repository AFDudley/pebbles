package pebbles

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// AppendEvent appends a single event to the events log.
func AppendEvent(root string, event Event) error {
	path := EventsPath(root)
	return withEventsFileLock(path, func() error {
		return appendEventsLocked(path, []Event{event})
	})
}

// appendEventsLocked appends events to the events log at path in order,
// assuming the caller already holds the events-file lock (withEventsFileLock)
// across this call — it takes no lock of its own, so calling it outside one
// races every other reader and writer of the log. A concurrent reader can
// therefore never observe a torn line between two of these writes, since
// readEventsFile blocks on the same lock for the whole span.
func appendEventsLocked(path string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open events log: %w", err)
	}
	defer func() { _ = file.Close() }()
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		data = append(data, '\n')
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("append event: %w", err)
		}
	}
	return nil
}

// LoadEvents reads all events from the events log.
func LoadEvents(root string) ([]Event, error) {
	return readEvents(EventsPath(root))
}

// readEvents reads events from a JSONL file path.
func readEvents(path string) ([]Event, error) {
	data, err := readEventsFile(path)
	if err != nil {
		return nil, err
	}
	return decodeEvents(data)
}

// readEventsFile reads the settled events log content under the same lock
// AppendEvent writes under (see withEventsFileLock).
func readEventsFile(path string) ([]byte, error) {
	var data []byte
	err := withEventsFileLock(path, func() error {
		var readErr error
		data, readErr = readEventsLocked(path)
		return readErr
	})
	if err != nil {
		return nil, fmt.Errorf("open events log: %w", err)
	}
	return data, nil
}

// readEventsLocked reads the events log's SETTLED content — appendEventsLocked's
// read-side sibling: the caller already holds whatever lock makes the read
// authoritative (or accepts a snapshot without one).
//
// Settled means truncated to the last terminating newline (settledEventData).
// pb's own flock cannot make a read torn-proof, because not every writer of
// .pebbles/events.jsonl is pb: pebble so-2a4's production tear happened with
// fd6e05a/so-b6a's reader+writer flock fully in place, torn by concurrent
// writers (a git merge driver, a dispatcher) that never take the .lock
// sibling. The reader itself must therefore be correct under a concurrent
// append, whoever the appender is.
func readEventsLocked(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return settledEventData(data), nil
}

// settledEventData returns the prefix of data covering only fully-appended
// events (pebble so-2a4). Every log writer terminates each event line with
// '\n' as the last byte of a single write, so content after the last newline
// is exactly one append still in flight — not yet part of the log, observable
// only mid-write. It is dropped here, at the read boundary, BEFORE any parse
// or digest: the same "wholly before the append" view the flock gives against
// pb's own writers, extended to writers that never take the lock. Nothing is
// ever lost — the log is append-only, so the next read after the append's
// newline lands sees the whole event. A malformed line that IS terminated
// (interior or final) is untouched by this trim and stays a loud parse error
// in decodeEvents/readEventLog: fully-appended garbage is corruption, not an
// append in flight.
func settledEventData(data []byte) []byte {
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data
	}
	last := bytes.LastIndexByte(data, '\n')
	return data[:last+1]
}

// decodeEvents decodes raw JSONL events log content.
//
// Decoding from bytes rather than a file handle lets a caller derive both the
// events and their digest from one read of the log, leaving no window for a
// concurrent append to land between the two (see EnsureCache).
func decodeEvents(data []byte) ([]Event, error) {
	// Scan the content line by line to decode JSONL records.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var events []Event
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Decode each event line into the Event struct.
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("parse event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan events log: %w", err)
	}
	return events, nil
}
