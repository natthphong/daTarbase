# daTarbase 🐘

**Build a PostgreSQL-style database in Go — one LeetCode-style problem at a time.**

Every function is a problem: a precise statement in its doc comment, an empty body
(`panic("TODO")`), and unit tests with real test data. Solve them all and the pieces
assemble into a small Postgres-like engine: 8KB slotted pages, a buffer pool, heap
files, B+Tree indexes (page split, INCLUDE / covering), a Volcano executor, sorting &
aggregation, inner/left/right/full/semi/anti joins, MVCC transactions with
commit/rollback, and a write-ahead log with crash recovery. Phase 11 glues every
phase into one engine with a Go API — `go run ./cmd/demo` shows it end to end
(on the `solution` branch, or once you have solved everything).

## How to use

```bash
make p P=2.3          # run the tests of one problem (Problem 2.3)
make phase P=02       # run every test of a phase
make progress         # ✅ / ⬜ / ❌ for every problem
make test             # everything
```

1. Pick a problem from the index below and click it — it jumps to the function.
2. Read the doc comment. It contains everything you need; the tests only check it.
3. Replace `panic("TODO: Problem X.Y")` with your implementation.
4. `make p P=X.Y` until green.

Tests of a problem only use provided code and **earlier problems of the same phase**,
so phases 1–10 can be done in any order. Within a phase, go in order.
Phase 11 (the engine) imports all other phases, so do it last.

## Branches

| Branch | Contents |
|---|---|
| `main` | the problem set — bodies are `panic("TODO")` |
| `solution` | reference solutions. Stuck? `git diff main solution -- phase02_page/page.go` |

## Layout

```
pkg/types      shared values, rows, schema, PageID / TID / TxnID / LSN   (provided)
pkg/exec       Volcano Iterator interface + Values() + Collect()          (provided)
pkg/testkit    R(1,"a",nil), AssertRows ... test helpers                  (provided)
cmd/demo       narrated end-to-end demo of the finished engine
phaseNN_*/     the problems — README.md in each phase is a deep-dive on how Postgres does it
tools/dbtool   generates this README, runs `make progress`
```

See [CONVENTIONS.md](CONVENTIONS.md) for how problems and tests are written.
