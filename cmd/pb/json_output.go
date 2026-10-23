package main

import (
	"encoding/json"
	"fmt"

	"pebbles/internal/pebbles"
)

// issueJSON describes the JSON payload for list/ready issue output.
type issueJSON struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	IssueType   string   `json:"type"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	ClosedAt    string   `json:"closed_at"`
	Deps        []string `json:"deps"`
}

// issueCommentJSON represents a single comment entry in JSON output.
//
// Payload is omitempty, so an ordinary comment's JSON is unchanged byte-for-byte.
type issueCommentJSON struct {
	Body      string            `json:"body"`
	Timestamp string            `json:"timestamp"`
	Payload   map[string]string `json:"payload,omitempty"`
}

// issueDetailJSON describes the JSON payload for pb show output.
type issueDetailJSON struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	IssueType   string             `json:"type"`
	Status      string             `json:"status"`
	Priority    string             `json:"priority"`
	CreatedAt   string             `json:"created_at"`
	UpdatedAt   string             `json:"updated_at"`
	ClosedAt    string             `json:"closed_at"`
	Deps        []string           `json:"deps"`
	Parents     []string           `json:"parents"`
	Siblings    []string           `json:"siblings"`
	Children    []string           `json:"children"`
	Comments    []issueCommentJSON `json:"comments"`
}

// buildIssueJSON converts an issue and its deps into the list/ready JSON shape.
func buildIssueJSON(issue pebbles.Issue, deps []string) issueJSON {
	// Ensure deps always encodes as an array instead of null.
	if deps == nil {
		deps = []string{}
	}
	return issueJSON{
		ID:          issue.ID,
		Title:       issue.Title,
		Description: issue.Description,
		IssueType:   issue.IssueType,
		Status:      issue.Status,
		Priority:    pebbles.PriorityLabel(issue.Priority),
		CreatedAt:   issue.CreatedAt,
		UpdatedAt:   issue.UpdatedAt,
		ClosedAt:    issue.ClosedAt,
		Deps:        deps,
	}
}

// buildIssueDetailJSON converts an issue, deps, and comments into show output.
func buildIssueDetailJSON(issue pebbles.Issue, deps []string, hierarchy pebbles.IssueHierarchy, comments []pebbles.IssueComment) issueDetailJSON {
	// Mirror the list/ready fields and attach the full comment history.
	if deps == nil {
		deps = []string{}
	}
	return issueDetailJSON{
		ID:          issue.ID,
		Title:       issue.Title,
		Description: issue.Description,
		IssueType:   issue.IssueType,
		Status:      issue.Status,
		Priority:    pebbles.PriorityLabel(issue.Priority),
		CreatedAt:   issue.CreatedAt,
		UpdatedAt:   issue.UpdatedAt,
		ClosedAt:    issue.ClosedAt,
		Deps:        deps,
		Parents:     issueIDsFromIssues(hierarchy.Parents),
		Siblings:    issueIDsFromIssues(hierarchy.Siblings),
		Children:    issueIDsFromIssues(hierarchy.Children),
		Comments:    buildIssueCommentsJSON(comments),
	}
}

// issueIDsFromIssues extracts issue IDs for JSON output.
func issueIDsFromIssues(issues []pebbles.Issue) []string {
	if len(issues) == 0 {
		return []string{}
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// buildIssueCommentsJSON converts issue comments to JSON-friendly structs.
func buildIssueCommentsJSON(comments []pebbles.IssueComment) []issueCommentJSON {
	if len(comments) == 0 {
		return []issueCommentJSON{}
	}
	converted := make([]issueCommentJSON, 0, len(comments))
	for _, comment := range comments {
		converted = append(converted, issueCommentJSON{
			Body:      comment.Body,
			Timestamp: comment.Timestamp,
			Payload:   comment.Payload,
		})
	}
	return converted
}

// issueJSONEntries builds the list/ready JSON payload for a set of issues,
// loading every issue's deps in ONE pass. The per-issue form this replaced
// called pebbles.GetIssue per entry, and each of those re-read and re-hashed
// the entire events log (see pebbles.ListIssueDepsAll).
func issueJSONEntries(root string, issues []pebbles.Issue) ([]issueJSON, error) {
	depsByID, err := pebbles.ListIssueDepsAll(root, issueIDsFromIssues(issues))
	if err != nil {
		return nil, fmt.Errorf("get deps: %w", err)
	}
	entries := make([]issueJSON, 0, len(issues))
	for _, issue := range issues {
		entries = append(entries, buildIssueJSON(issue, depsByID[issue.ID]))
	}
	return entries, nil
}

// printJSON marshals the provided payload and writes it to stdout.
func printJSON(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
