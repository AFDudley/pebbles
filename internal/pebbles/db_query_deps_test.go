package pebbles

import (
	"testing"
)

// depsFixture builds a project exercising every shape ListIssueDepsAll has to
// reproduce: an issue with several blockers, an issue with one, an issue with
// none, a parent-child edge that must NOT appear as a blocking dep, and an
// issue reached through a two-hop rename chain.
func depsFixture(t *testing.T) (root string, ids []string) {
	t.Helper()
	root = t.TempDir()
	if err := InitProject(root); err != nil {
		t.Fatalf("init project: %v", err)
	}
	created := []struct{ id, at string }{
		{"pb-a", "2024-01-01T00:00:00Z"},
		{"pb-b", "2024-01-01T00:01:00Z"},
		{"pb-c", "2024-01-01T00:02:00Z"},
		{"pb-d", "2024-01-01T00:03:00Z"},
		{"pb-e", "2024-01-01T00:04:00Z"},
		{"pb-old", "2024-01-01T00:05:00Z"},
	}
	for _, issue := range created {
		event := NewCreateEvent(issue.id, "Title "+issue.id, "", "task", issue.at, 2)
		if err := AppendEvent(root, event); err != nil {
			t.Fatalf("append create %s: %v", issue.id, err)
		}
	}
	edges := []struct {
		issue   string
		blocker string
		depType string
	}{
		{"pb-a", "pb-c", DepTypeBlocks},
		{"pb-a", "pb-b", DepTypeBlocks},
		{"pb-b", "pb-c", DepTypeBlocks},
		{"pb-d", "pb-e", DepTypeParentChild},
		{"pb-old", "pb-e", DepTypeBlocks},
	}
	for _, edge := range edges {
		event := NewDepAddEvent(edge.issue, edge.blocker, edge.depType, "2024-01-02T00:00:00Z")
		if err := AppendEvent(root, event); err != nil {
			t.Fatalf("append dep %s->%s: %v", edge.issue, edge.blocker, err)
		}
	}
	// Two rename hops, so a resolver that follows only one edge is caught.
	if err := AppendEvent(root, NewRenameEvent("pb-old", "pb-mid", "2024-01-03T00:00:00Z")); err != nil {
		t.Fatalf("append first rename: %v", err)
	}
	if err := AppendEvent(root, NewRenameEvent("pb-mid", "pb-new", "2024-01-03T00:01:00Z")); err != nil {
		t.Fatalf("append second rename: %v", err)
	}
	if err := EnsureCache(root); err != nil {
		t.Fatalf("rebuild cache: %v", err)
	}
	return root, []string{"pb-a", "pb-b", "pb-c", "pb-d", "pb-e", "pb-new", "pb-old", "pb-mid"}
}

// TestListIssueDepsAllMatchesGetIssue is the equivalence oracle for the batch
// path: for every ID, the one-pass result must be what the per-issue GetIssue
// path returned, element for element. `pb list --json` and `pb ready --json`
// render deps from the batch path now, so any divergence here is a silent
// change in their output.
func TestListIssueDepsAllMatchesGetIssue(t *testing.T) {
	root, ids := depsFixture(t)

	batch, err := ListIssueDepsAll(root, ids)
	if err != nil {
		t.Fatalf("list issue deps all: %v", err)
	}
	if len(batch) != len(ids) {
		t.Fatalf("batch covered %d ids, want %d", len(batch), len(ids))
	}
	for _, id := range ids {
		_, want, err := GetIssue(root, id)
		if err != nil {
			t.Fatalf("get issue %s: %v", id, err)
		}
		got, ok := batch[id]
		if !ok {
			t.Fatalf("batch result is missing %s", id)
		}
		if len(got) != len(want) {
			t.Fatalf("deps for %s: got %v, per-issue path returned %v", id, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("deps for %s at %d: got %q, per-issue path returned %q (got=%v want=%v)",
					id, i, got[i], want[i], got, want)
			}
		}
	}
}

// TestListIssueDepsAllResolvesRenames pins the two behaviours the equivalence
// check above depends on but would still pass without if the fixture ever
// lost them: a renamed-away ID yields its successor's blockers rather than
// none, and parent-child edges never leak into the blocking list.
func TestListIssueDepsAllResolvesRenames(t *testing.T) {
	root, _ := depsFixture(t)

	deps, err := ListIssueDepsAll(root, []string{"pb-old", "pb-new", "pb-d", "pb-e"})
	if err != nil {
		t.Fatalf("list issue deps all: %v", err)
	}
	// pb-old was renamed to pb-mid then pb-new; its blocker moved with it.
	if len(deps["pb-old"]) != 1 || deps["pb-old"][0] != "pb-e" {
		t.Fatalf("renamed-away id lost its deps: pb-old=%v, want [pb-e]", deps["pb-old"])
	}
	if len(deps["pb-new"]) != 1 || deps["pb-new"][0] != "pb-e" {
		t.Fatalf("current id lost its deps: pb-new=%v, want [pb-e]", deps["pb-new"])
	}
	// pb-d -> pb-e is parent-child, which is not a blocking dependency.
	if len(deps["pb-d"]) != 0 {
		t.Fatalf("parent-child edge leaked into blocking deps: pb-d=%v", deps["pb-d"])
	}
	if len(deps["pb-e"]) != 0 {
		t.Fatalf("pb-e has no dependencies, got %v", deps["pb-e"])
	}
}

// TestListIssueDepsAllChecksCacheOnce is the N+1 oracle. EnsureCache reads and
// sha256s the entire events log, and the per-issue path ran it once per issue;
// the output was byte-identical either way, so only the call count
// distinguishes them. Asserting the count does not GROW with the issue count
// tests the algorithm rather than the machine — unlike a wall-clock threshold,
// which would be flaky and would pass on a fast enough disk.
func TestListIssueDepsAllChecksCacheOnce(t *testing.T) {
	root, ids := depsFixture(t)

	before := CacheChecks()
	if _, err := ListIssueDepsAll(root, ids); err != nil {
		t.Fatalf("list issue deps all: %v", err)
	}
	if got := CacheChecks() - before; got != 1 {
		t.Fatalf("ListIssueDepsAll checked the cache %d times for %d ids, want exactly 1", got, len(ids))
	}
}
