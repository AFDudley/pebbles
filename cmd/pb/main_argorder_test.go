package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"testing"

	"pebbles/internal/pebbles"
)

// argOrderProject builds a two-issue project for the argument-order checks.
func argOrderProject(t *testing.T) (root, first, second string) {
	t.Helper()
	root = t.TempDir()
	if err := pebbles.InitProject(root); err != nil {
		t.Fatalf("init project: %v", err)
	}
	first, second = "pb-one", "pb-two"
	events := []pebbles.Event{
		pebbles.NewCreateEvent(first, "First", "", "task", "2024-01-01T00:00:00Z", 2),
		pebbles.NewCreateEvent(second, "Second", "", "task", "2024-01-01T00:01:00Z", 2),
	}
	for _, event := range events {
		if err := pebbles.AppendEvent(root, event); err != nil {
			t.Fatalf("append create: %v", err)
		}
	}
	if err := pebbles.EnsureCache(root); err != nil {
		t.Fatalf("rebuild cache: %v", err)
	}
	return root, first, second
}

// TestShowAcceptsFlagsAfterIssueID is the regression oracle for the wiring of
// reorderFlags into runShow. Go's flag package stops at the first positional,
// so `pb show <id> --json` used to drop --json AND count the id as a surplus
// argument, failing with "show requires issue id" — on the most-used pb
// subcommand. Both spellings are checked because the flag package accepts
// -json as readily as --json.
func TestShowAcceptsFlagsAfterIssueID(t *testing.T) {
	root, id, _ := argOrderProject(t)

	for _, spelling := range []string{"--json", "-json"} {
		out := captureStdout(t, func() {
			runShow(root, []string{id, spelling})
		})
		var got struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("pb show %s %s did not emit JSON: %v; output=%q", id, spelling, err, out)
		}
		if got.ID != id {
			t.Fatalf("pb show %s %s reported id %q", id, spelling, got.ID)
		}
	}
}

// TestCommentAcceptsSingleDashValueFlag covers the other half of the defect.
// runComment was already routed through reorderFlags, but with a hand-written
// {"--body": true} map, so with the -body spelling the map did not mark it as
// value-taking and the body text was left behind as a second positional —
// "scan issue: sql: no rows in result set", the comment silently not written.
// Deriving the map from the FlagSet is what makes both spellings work.
func TestCommentAcceptsSingleDashValueFlag(t *testing.T) {
	root, id, _ := argOrderProject(t)

	captureStdout(t, func() {
		runComment(root, []string{id, "-body", "recorded"})
	})

	comments, err := pebbles.ListIssueComments(root, id)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "recorded" {
		t.Fatalf("pb comment %s -body recorded stored %+v", id, comments)
	}
}

// TestDepAddAcceptsSingleDashValueFlag is the same defect on `pb dep add`,
// whose hand-written map also listed only --type. With -type the dependency
// type string became a third positional and the command died on arity.
func TestDepAddAcceptsSingleDashValueFlag(t *testing.T) {
	root, first, second := argOrderProject(t)

	captureStdout(t, func() {
		runDep(root, []string{"add", second, first, "-type", pebbles.DepTypeParentChild})
	})

	hierarchy, err := pebbles.GetIssueHierarchy(root, second)
	if err != nil {
		t.Fatalf("get hierarchy: %v", err)
	}
	if len(hierarchy.Parents) != 1 || hierarchy.Parents[0].ID != first {
		t.Fatalf("parent-child edge not recorded: parents=%+v", hierarchy.Parents)
	}
}

// TestUpdateAcceptsFlagsAfterIssueID pins runUpdate, which previously carried
// its own partial reordering — move args[0] to the end — instead of using
// reorderFlags. That covered `pb update <id> --status x` and nothing else: it
// only moves a LEADING positional, so an id with flags on BOTH sides of it
// still stopped the parse dead and lost every flag after the id.
func TestUpdateAcceptsFlagsAfterIssueID(t *testing.T) {
	root, id, _ := argOrderProject(t)

	captureStdout(t, func() {
		runUpdate(root, []string{"--title", "Renamed", id, "--type", "bug"})
	})

	issue, _, err := pebbles.GetIssue(root, id)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Title != "Renamed" || issue.IssueType != "bug" {
		t.Fatalf("update dropped a flag: title=%q type=%q", issue.Title, issue.IssueType)
	}
}

// TestValueTakingFlagsCoversBothSpellings pins the derivation that makes the
// checks above hold for every flag rather than the ones someone remembered to
// list. Booleans must stay excluded: marking --json as value-taking would
// swallow the argument after it, turning `pb show --json <id>` into a lookup
// of nothing.
func TestValueTakingFlagsCoversBothSpellings(t *testing.T) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.String("body", "", "value flag")
	fs.Int("limit", 0, "value flag")
	fs.Bool("json", false, "boolean flag")
	var custom optionalString
	fs.Var(&custom, "title", "custom value flag")

	got := valueTakingFlags(fs)
	for _, name := range []string{"-body", "--body", "-limit", "--limit", "-title", "--title"} {
		if !got[name] {
			t.Errorf("%s takes a value but was not marked as such", name)
		}
	}
	for _, name := range []string{"-json", "--json"} {
		if got[name] {
			t.Errorf("%s is boolean but was marked as taking a value", name)
		}
	}
}

// TestListJSONMatchesPerIssueDeps is the call-site oracle for the batched deps
// path. `pb list --json` and `pb ready --json` render deps from
// pebbles.ListIssueDepsAll now; this pins their output to what the per-issue
// pebbles.GetIssue path returns, so a divergence in the batch path fails here
// rather than silently changing what every consumer of the JSON reads.
func TestListJSONMatchesPerIssueDeps(t *testing.T) {
	root, first, second := argOrderProject(t)
	dep := pebbles.NewDepAddEvent(second, first, pebbles.DepTypeBlocks, "2024-01-02T00:00:00Z")
	if err := pebbles.AppendEvent(root, dep); err != nil {
		t.Fatalf("append dep: %v", err)
	}
	if err := pebbles.EnsureCache(root); err != nil {
		t.Fatalf("rebuild cache: %v", err)
	}

	out := captureStdout(t, func() {
		runList(root, []string{"--all", "--json"})
	})
	var entries []struct {
		ID   string   `json:"id"`
		Deps []string `json:"deps"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("unmarshal list json: %v; output=%q", err, out)
	}
	if len(entries) == 0 {
		t.Fatalf("list --all --json returned nothing")
	}
	depCount := 0
	for _, entry := range entries {
		_, want, err := pebbles.GetIssue(root, entry.ID)
		if err != nil {
			t.Fatalf("get issue %s: %v", entry.ID, err)
		}
		depCount += len(entry.Deps)
		if len(entry.Deps) != len(want) {
			t.Fatalf("deps for %s: json has %v, per-issue path has %v", entry.ID, entry.Deps, want)
		}
		for i := range want {
			if entry.Deps[i] != want[i] {
				t.Fatalf("deps for %s: json has %v, per-issue path has %v", entry.ID, entry.Deps, want)
			}
		}
	}
	// Without this the comparison above would pass on an all-empty payload.
	if depCount != 1 {
		t.Fatalf("fixture is vacuous: expected exactly one dep across the payload, got %d", depCount)
	}
}

// TestListJSONChecksCacheOnce is the N+1 oracle at the call site. The payload
// is byte-identical whether deps are loaded per issue or in one pass, so the
// only thing separating the two is how many times the whole events log is
// re-read and re-hashed. Asserting the count does not grow with the issue
// count tests the algorithm; a wall-clock threshold would be flaky and would
// test the machine.
func TestListJSONChecksCacheOnce(t *testing.T) {
	project := func(issueCount int) string {
		root := t.TempDir()
		if err := pebbles.InitProject(root); err != nil {
			t.Fatalf("init project: %v", err)
		}
		for i := range issueCount {
			id := fmt.Sprintf("pb-%03d", i)
			event := pebbles.NewCreateEvent(id, "Title", "", "task", "2024-01-01T00:00:00Z", 2)
			if err := pebbles.AppendEvent(root, event); err != nil {
				t.Fatalf("append create: %v", err)
			}
		}
		if err := pebbles.EnsureCache(root); err != nil {
			t.Fatalf("rebuild cache: %v", err)
		}
		return root
	}
	checks := func(root string) uint64 {
		before := pebbles.CacheChecks()
		captureStdout(t, func() { runList(root, []string{"--all", "--json"}) })
		return pebbles.CacheChecks() - before
	}

	small := checks(project(2))
	large := checks(project(12))
	if small != large {
		t.Fatalf("cache checks scale with issue count: 2 issues -> %d checks, 12 issues -> %d; "+
			"the JSON arm is re-reading the events log once per issue", small, large)
	}
}
