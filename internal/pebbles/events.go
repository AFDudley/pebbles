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
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	data = append(data, '\n')
	return withEventsFileLock(path, func() error {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open events log: %w", err)
		}
		defer func() { _ = file.Close() }()
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("append event: %w", err)
		}
		return nil
	})
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

// readEventsFile reads the raw events log content under the same lock
// AppendEvent writes under (see withEventsFileLock).
func readEventsFile(path string) ([]byte, error) {
	var data []byte
	err := withEventsFileLock(path, func() error {
		var readErr error
		data, readErr = os.ReadFile(path)
		return readErr
	})
	if err != nil {
		return nil, fmt.Errorf("open events log: %w", err)
	}
	return data, nil
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
