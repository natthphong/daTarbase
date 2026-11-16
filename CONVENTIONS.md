# Problem-set conventions

Every phase of daTarbase is a LeetCode-style problem set. Contributors (human or AI)
MUST follow these rules — `go run ./tools/dbtool check` and the template generator
depend on them.

## Repository layout

```
go.mod                      module github.com/natthphong/daTarbase (Go 1.26, stdlib only)
pkg/types                   PROVIDED shared vocabulary: Value, Row, Schema, PageID, TID, TxnID, LSN, PageSize
pkg/exec                    PROVIDED Volcano Iterator interface + Values() source + Collect()
pkg/testkit                 PROVIDED test helpers: R(1,"a",nil), Rows(...), AssertRows, AssertRowsUnordered
phaseNN_name/               one Go package per phase (package name = the part after "phaseNN_")
  README.md                 phase notes (see below)
  *.go                      provided code + problems (solution lives in the body)
  *_test.go                 tests
tools/dbtool                README / progress / template generator
```

**Never edit `pkg/`, `tools/`, `go.mod`, or another phase's directory.** If you need
a helper that isn't in `pkg/`, put it inside your own phase (unexported, or a provided
exported helper).

No third-party modules. Standard library only.

## What a problem looks like

A problem is a **function or method** whose doc comment starts with a marker line:

```go
// Problem 2.3 — InsertTuple [Medium]
//
// <one-sentence summary — this line becomes the README "What you build" column>
//
// <explanation of the concept, with an ASCII diagram if it's about layout>
//
// Rules:
//  1. ...precise, testable rules. Every edge case the tests check MUST be stated here.
//
// Example:
//
//	<concrete input → output, using the same numbers as the tests>
//
// Constraints: <complexity / allocation / concurrency requirements>
// Postgres reference: <function name + source file, when one exists>
func (p *Page) InsertTuple(data []byte) (SlotID, error) {
	// full working SOLUTION goes here (on the solution branch)
}
```

- Marker regex: `^Problem (\d+)\.(\d+) — (\S+) \[(Easy|Medium|Hard)\]$` (em dash `—`).
- Numbering inside a phase is **contiguous from 1** (2.1, 2.2, …) in the order a learner
  should solve them. Easier/foundational first.
- Problem statements are in **English**. Clear enough that someone can solve it
  **without reading the tests**. Spell out byte orders, offsets, error values, tie-breaks.
- The doc comment of a problem is what the learner sees. Don't leak the algorithm's
  code in it; hints are fine ("Hint: think of a min-heap of run heads").
- Each problem is ONE function/method. Aim for 10–80 lines of solution.

### Provided code vs problem code

Code without a marker is **provided** and stays as-is on the template branch:
type definitions, constants, errors, constructors, small accessors, fakes used by tests.
Keep provided code small but sufficient — the learner fills in only problem bodies.

- Struct fields the solution needs MUST be declared in provided types (the learner may add
  more fields). Comment each field.
- If the solution needs a private helper function, either inline it as a closure in the
  problem body, or put `// solution-only` in the helper's doc comment — the template
  generator deletes such decls on the template branch. Do NOT let provided code call a
  `solution-only` helper.
- The template generator replaces every problem body with `panic("TODO: Problem X.Y")`
  and removes imports that become unused. **The template must still compile**, so provided
  code must never depend on anything only defined inside a problem body.

## Tests

- Test function names: `TestP<phase>_<num>_<Anything>` e.g. `TestP2_3_InsertTuple`,
  `TestP2_3_InsertTuple_ByteLayout`. Every problem needs ≥ 1 test (≥ 3 for Medium/Hard).
- `go test -run '^TestP2_3_' ./phase02_page/` must test exactly problem 2.3.
- A test for problem N may call: provided code, problem N, and problems < N **in the same
  phase**. Never a later problem. Never another phase's problem (another phase's
  provided code is OK but prefer self-contained fakes).
- Table-driven, with **concrete, meaningful test data** (named cases like
  `"two tuples grow toward each other"`). Include edge cases: empty input, exact fit,
  off-by-one at the boundary, NULLs, duplicates, errors leave state unchanged.
- Where the problem is about bytes on disk, check the exact bytes (golden layout tests).
- Where it's about I/O efficiency, assert counts (pages read/written, rows pulled) using
  instrumented fakes.
- Use `pkg/testkit` for rows: `testkit.R(1, "alice", nil)`, `testkit.AssertRows`.
- Use `t.TempDir()` for files. Tests must be deterministic (seeded `math/rand/v2` only), fast
  (< 2s per package) and pass with `go test -race`.
- Failure messages must say what was expected: `t.Fatalf("lower = %d, want %d", ...)`.

## Phase README.md

```
# Phase N — Title

One paragraph: what this phase builds and how it fits into the database.
(First paragraph is copied into the root README.)

## Concepts
Short deep-dive (bullets / diagrams) on how Postgres does it.

## Prerequisites
Which earlier phases (if any) this phase's code imports.

## Suggested order
...
```

## Definition of done for a phase

```
go vet ./phaseNN_name/
go test -race -count=1 ./phaseNN_name/      # all green WITH solutions
go run ./tools/dbtool check                  # your phase reports no issues
```
and the template version (bodies replaced by panic) must compile:
`go vet` on a copy after `go run ./tools/dbtool template`.
