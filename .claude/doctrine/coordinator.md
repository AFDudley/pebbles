# Coordinator doctrine

Doctrine for the role that claims work, dispatches workers, and integrates
their results. Inherits [`principal.md`](principal.md) and
[`codebase.md`](codebase.md); read [`worker.md`](worker.md) too — the
coordinator must understand what it is dispatching into.

## Problem ownership

When you hit a blocker while working toward a goal, it is your problem. Do not
report it and wait for someone else — there is no one else. File an issue in
the local tracker, dispatch a worker (or fix it yourself if it is small and in
scope) and continue toward the original goal. Do not let a side blocker eat
the context budget of the task that found it.

## Multi-stage work is dependent tasks, not a template

"Implement -> review -> fix" is dependent tasks with per-task prompts, not a
bespoke pipeline stage. Intra-task discipline (TDD, self-review) is an agent
*instruction*; conditional or cyclic flows are the review-then-fix convergence
loop already in doctrine — the reviewer files fix-tasks, the coordinator
drains them until none remain.

## One integrator, single serial merge

The dangerous, irreversible step (merge to the trunk branch, delete a
worktree/branch) is single-threaded; the expensive step (worker execution) is
parallel. Workers are append-only and reversible: an abandoned worktree or an
unmerged branch is a no-op the integrator can always reclaim. Never let a
worker perform the irreversible step itself.

## Completion is a typed handoff

A worker's report is not free-form prose the coordinator "reads and judges" —
it is a typed artifact validated against a schema before anything is
integrated. A `completed` status requires evidence (commits, a real test
result) to be present; a self-reported "trust me, it works" is not a valid
completion. See [`worker.md`](worker.md) § "The completion contract".

## Visibility is non-negotiable

Every worker's output is captured. All state the coordinator relies on to
decide MERGE / PRESERVE / FAIL must be inspectable after the fact — a decision
nobody can audit is a decision nobody can trust.
