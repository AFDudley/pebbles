package pebbles

import "strings"

// Event represents an append-only change in the Pebbles log.
type Event struct {
	Type      string            `json:"type"`
	Timestamp string            `json:"timestamp"`
	IssueID   string            `json:"issue_id"`
	Payload   map[string]string `json:"payload"`
}

// Issue represents the current state of a Pebbles issue.
type Issue struct {
	ID          string
	Title       string
	Description string
	IssueType   string
	Status      string
	Priority    int
	CreatedAt   string
	UpdatedAt   string
	ClosedAt    string
}

// IssueComment represents a user-authored comment on an issue.
//
// Payload carries the comment event's payload keys OTHER than "body" — the typed
// extension channel described in
// docs/decisions/2026-08-07-intake-provenance-is-a-capability-not-a-string.md.
// Event.Payload is a map decoded non-strictly (events.go decodeEvents), and
// applyComment validates only "body", so extra keys are inert to the reducer
// and survive verbatim in the log. This is why a machine-readable record rides
// a comment payload rather than a NEW event type: applyEvent's default branch
// (db_apply.go) hard-fails on an unknown type, and that failure propagates
// through EnsureCache to every verb, permanently and without self-healing.
type IssueComment struct {
	IssueID   string
	Body      string
	Timestamp string
	Payload   map[string]string
}

// IssueHierarchyItem represents an issue with its indentation depth.
type IssueHierarchyItem struct {
	Issue Issue
	Depth int
}

// BlockedIssue represents an issue and its open blockers.
type BlockedIssue struct {
	Issue    Issue
	Blockers []Issue
}

// Config stores per-project Pebbles settings.
type Config struct {
	Prefix string `json:"prefix"`
}

const (
	// EventTypeCreate indicates a create event.
	EventTypeCreate = "create"
	// EventTypeTitleUpdated indicates an issue title update event.
	EventTypeTitleUpdated = "title_updated"
	// EventTypeStatus indicates a status update event.
	EventTypeStatus = "status_update"
	// EventTypeUpdate indicates an update to issue fields.
	EventTypeUpdate = "update"
	// EventTypeClose indicates a close event.
	EventTypeClose = "close"
	// EventTypeComment indicates a comment event.
	EventTypeComment = "comment"
	// EventTypeRename indicates an issue ID rename event.
	EventTypeRename = "rename"
	// EventTypeDepAdd indicates a dependency add event.
	EventTypeDepAdd = "dep_add"
	// EventTypeDepRemove indicates a dependency removal event.
	EventTypeDepRemove = "dep_rm"
)

const (
	// DepTypeBlocks indicates a blocking dependency.
	DepTypeBlocks = "blocks"
	// DepTypeParentChild indicates a parent-child relationship.
	DepTypeParentChild = "parent-child"
)

const (
	// StatusOpen indicates an open issue.
	StatusOpen = "open"
	// StatusInProgress indicates an in-progress issue.
	StatusInProgress = "in_progress"
	// StatusClosed indicates a closed issue.
	StatusClosed = "closed"
)

// NormalizeDepType returns a normalized dependency type with a default.
func NormalizeDepType(depType string) string {
	trimmed := strings.TrimSpace(depType)
	if trimmed == "" {
		return DepTypeBlocks
	}
	return trimmed
}
