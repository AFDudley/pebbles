package pebbles

import "bytes"

// maxEventLineBytes is the largest single events.jsonl line pb will parse,
// the same buffer size beads_import.go sets for the Beads issue importer.
const maxEventLineBytes = 5 * 1024 * 1024

// OversizedEventLine names one line of the events log that was too large to
// read: it exceeded maxEventLineBytes and was skipped rather than parsed.
type OversizedEventLine struct {
	Line int
	Size int
}

// numberedEventLine pairs a trimmed, non-empty events.jsonl line with its
// 1-based line number.
type numberedEventLine struct {
	Number int
	Text   []byte
}

// splitEventLines splits raw events.jsonl content into its non-empty lines,
// each paired with its 1-based line number. A line longer than
// maxEventLineBytes is reported in oversized instead of lines, so one
// oversized line does not by itself stop the rest of the log from being
// split and read.
func splitEventLines(data []byte) (lines []numberedEventLine, oversized []OversizedEventLine) {
	lineNumber := 0
	for len(data) > 0 {
		lineNumber++
		var raw []byte
		if idx := bytes.IndexByte(data, '\n'); idx == -1 {
			raw = data
			data = nil
		} else {
			raw = data[:idx]
			data = data[idx+1:]
		}
		text := bytes.TrimSpace(raw)
		if len(text) == 0 {
			continue
		}
		if len(text) > maxEventLineBytes {
			oversized = append(oversized, OversizedEventLine{Line: lineNumber, Size: len(text)})
			continue
		}
		lines = append(lines, numberedEventLine{Number: lineNumber, Text: text})
	}
	return lines, oversized
}
