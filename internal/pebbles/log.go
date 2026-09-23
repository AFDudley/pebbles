package pebbles

import (
	"encoding/json"
	"fmt"
)

// EventLogEntry pairs an event with its line number in the log.
type EventLogEntry struct {
	Line  int
	Event Event
}

// LoadEventLog reads the event log and returns entries with line numbers,
// plus any line too large to read (see OversizedEventLine).
func LoadEventLog(root string) ([]EventLogEntry, []OversizedEventLine, error) {
	return readEventLog(EventsPath(root))
}

// readEventLog reads a JSONL log file and records line numbers for each
// event. A line over maxEventLineBytes is reported back in oversized rather
// than treated as a read failure, so the rest of the log still comes back.
func readEventLog(path string) ([]EventLogEntry, []OversizedEventLine, error) {
	data, err := readEventsFile(path)
	if err != nil {
		return nil, nil, err
	}
	lines, oversized := splitEventLines(data)
	var entries []EventLogEntry
	for _, line := range lines {
		// Decode JSON into an Event, reporting the line on errors.
		var event Event
		if err := json.Unmarshal(line.Text, &event); err != nil {
			return nil, nil, fmt.Errorf("parse event line %d: %w", line.Number, err)
		}
		entries = append(entries, EventLogEntry{Line: line.Number, Event: event})
	}
	return entries, oversized, nil
}
