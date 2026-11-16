# Phase 8 — Joins

The three physical join algorithms of the executor — nested loop, hash join and merge join — for every SQL join type (INNER, LEFT, RIGHT, FULL, SEMI, ANTI), plus the NULL trap of `NOT IN`, Grace hash partitioning for inputs bigger than memory, an index nested loop, and the planner side: estimating a join's row count and choosing the cheapest join order with Selinger's dynamic programming.

## Concepts

### One logical join, three physical algorithms
SQL says *what* to join (`JoinType` + the ON clause); the planner picks *how*. In
`EXPLAIN` you see one of three nodes, each from its own executor file:

| Node | File | Needs | Cost | Join types |
|---|---|---|---|---|
| Nested Loop | `nodeNestloop.c` | nothing — any condition, even `a < b` | \|outer\|·\|inner\| (or \|outer\|·index lookup) | INNER, LEFT, SEMI, ANTI |
| Hash Join | `nodeHashjoin.c` + `nodeHash.c` | at least one `=` clause with a hash opclass | \|outer\| + \|inner\|, memory for the inner | all |
| Merge Join | `nodeMergejoin.c` | `=` clauses with btree opclass; both inputs sorted | \|outer\| + \|inner\| (+ sorts) | all (FULL only with merge-joinable clauses) |

The planner considers all of them for every pair of relations (`add_paths_to_joinrel`
in `joinpath.c`) and keeps the cheapest paths.

### The ON clause: join quals vs. other quals
- Postgres splits the ON clause into the part the algorithm uses to *find* candidates
  (`hashclauses`, `mergeclauses`, or an index's `Index Cond`) and the rest
  (`joinqual`). In this phase that is `JoinSpec.Keys` and `JoinSpec.Extra`.
- Both must be true for a pair to *match*. For outer joins this matters: an ON
  condition that fails produces a NULL-padded row; the same condition in WHERE would
  remove the row. (`LEFT JOIN d ON e.dept = d.id AND e.id <> 6` keeps frank, padded.)
- `NULL = x` is never true, so NULL keys never match — even `NULL = NULL`. Hash
  join simply doesn't insert NULL-key inner rows (unless it must emit them for
  RIGHT/FULL); merge join sorts NULLs to the end and stops/pads there.

### Nested loop
```
for each outer tuple o:        ExecReScan(inner) — rescan for every o
    for each inner tuple i:
        if quals(o, i): emit
```
- LEFT needs one bit (`nl_MatchedOuter`) per outer row. SEMI (`EXISTS`) and ANTI
  (`NOT EXISTS`) set `single_match` and stop scanning the inner at the first match.
- RIGHT/FULL would need a bit per *inner* row that survives across rescans;
  Postgres never plans a nested loop for them (`match_unsorted_outer`) — hash or merge
  join must be used. Problem 8.4 shows what that bookkeeping looks like.
- The rescan is cheap when the inner is a Material node (rows cached) or an **index
  scan parameterized by the outer row** — "Nested Loop → Index Scan, Index Cond:
  (d.id = e.dept_id)". That is the best plan when the outer side is small (Problem 8.12).

### NOT IN is not NOT EXISTS
`x NOT IN (subquery)` is `x <> v1 AND x <> v2 AND …`. One NULL `v` makes every
comparison UNKNOWN, so the predicate is never TRUE and the query returns nothing.
A NULL `x` is likewise never TRUE — unless the subquery is empty (`NOT IN ()` is TRUE).
Because of this, Postgres does not turn `NOT IN` into an anti join (it does so for
`NOT EXISTS`); it runs a
**hashed SubPlan**: load the subquery into a hash table once, remember whether a NULL
was seen (`ExecHashSubPlan`, `nodeSubplan.c`). Always prefer `NOT EXISTS`.

### Hash join
```
          Hash Join (hashclauses: e.dept_id = d.id)
          ├── Seq Scan employees   ← outer / probe
          └── Hash                 ← inner / build: the smaller input
              └── Seq Scan departments
```
- **Build**: `MultiExecHash` reads the whole inner into an in-memory table of
  buckets; **probe**: for each outer tuple compute the hash, walk the bucket, and
  re-check the hash clauses (different keys can share a hash value) and the joinqual.
- **Empty inner shortcut**: if the hash table is empty and the join cannot emit
  unmatched outer rows, the outer is never scanned (`HJ_BUILD_HASHTABLE`).
- **Outer joins**: LEFT pads the probe row on the spot. RIGHT/FULL mark every matched
  inner tuple (`HeapTupleHeaderSetMatch`) and finish with
  `ExecScanHashTableForUnmatched`. For these, NULL-key inner rows can never match
  but must still be remembered so they can be emitted padded.
- **Batches (Grace / hybrid hash)**: if the inner does not fit in
  `work_mem × hash_mem_multiplier`, the planner picks `nbatch` (a power of two). Each
  tuple's hash value is split into bucket bits and batch bits
  (`ExecHashGetBucketAndBatch`); tuples of batches ≠ 0 go to temp files on both sides
  and are joined batch by batch later (`ExecHashJoinNewBatch`). If a batch still
  overflows at run time, `ExecHashIncreaseNumBatches` doubles `nbatch` and moves
  tuples whose new batch bit is set — the "recursive repartition" of Problem 8.9. When
  doubling does not help (one key with thousands of duplicates), growth is disabled
  (`growEnabled = false`) and the batch simply exceeds work_mem. `EXPLAIN ANALYZE`
  shows `Buckets: … Batches: … Memory Usage: …`.
- Parallel hash join (PG 11) builds one shared table cooperatively.

### Merge join
```
left  (sorted): 10 10 20 20 30 40 NULL
right (sorted): 10 20 20 30 50 NULL
                    ^mark
```
- Advance whichever side has the smaller key. On equal keys, `ExecMarkPos` the inner
  at the start of its group; after each outer tuple of the group, `ExecRestrPos` back
  to the mark if the next outer tuple has the same key. The inner must support
  mark/restore — a Sort or Material node, or an index scan; otherwise the planner
  inserts a Material node.
- Its state machine (`EXEC_MJ_INITIALIZE_OUTER`, `EXEC_MJ_JOINTUPLES`,
  `EXEC_MJ_NEXTOUTER`, `EXEC_MJ_TESTOUTER`, `EXEC_MJ_SKIPOUTER_ADVANCE`, …,
  `EXEC_MJ_ENDOUTER`) emits unmatched rows as soon as it skips them (`MJFillOuter`,
  `MJFillInner`), so outer joins cost nothing extra.
- Output is sorted on the join key — useful for a later ORDER BY, GROUP BY or another
  merge join ("interesting orders" in the planner).

### Estimating join size
`eqjoinsel_inner` (`selfuncs.c`) uses pg_stats: without MCV lists the selectivity of
`R.a = S.b` is `(1 − nullfrac_a)(1 − nullfrac_b) / max(nd_a, nd_b)`, assuming the
smaller value set is contained in the larger one. With MCV lists on both sides it
first matches the most common values exactly, then applies the formula to the rest.
Row estimates are then clamped by `clamp_row_est` (≥ 1, rounded). Bad estimates here
are the #1 cause of bad plans: they compound with every additional join.

### Join order: Selinger's dynamic programming
- For n relations there are n! left-deep orders (and far more bushy trees). System R
  (Selinger et al. 1979) observed that the best way to join a *set* of relations does
  not depend on the order used inside it, so it computes the best plan for every
  subset, smallest first: `best(S) = min over t ∈ S of best(S − t) ⨝ t`. O(2ⁿ) states
  instead of n!.
- Postgres does the same level by level (`standard_join_search` →
  `join_search_one_level`), considers bushy plans too, avoids clauseless joins
  (cross products) when it can, and keeps several paths per set because of
  sort orders ("interesting orders") and parameterization.
- Above `geqo_threshold` (12) relations it switches to the genetic optimizer
  (GEQO), and `join_collapse_limit` / `from_collapse_limit` (8) bound how much
  of an explicit JOIN tree it may reorder.
- Our cost model is the textbook C_out: the sum of all intermediate result sizes.

## Prerequisites
None beyond the provided `pkg/types`, `pkg/exec` and `pkg/testkit`. The phase is
self-contained. All tests share one dataset (employees ⨝ departments, with a
department nobody works in, an employee with a NULL department, a department with a
NULL id, and key 20 duplicated on both sides) plus a 1000×1000 randomized input that
is cross-checked against a simple nested-loop oracle.

## Suggested order
1. **8.1 JoinSpec.Match** — the ON clause, with SQL NULL semantics.
2. **8.2 NestedLoopInner → 8.3 NestedLoopLeft → 8.4 NestedLoopRightFull → 8.5 NestedLoopSemiAnti** — every join type with the simplest algorithm.
3. **8.6 NotIn** — the NULL trap.
4. **8.7 HashJoinInner → 8.8 HashJoinOuter → 8.9 GraceHashJoin** — hashing, matched flags, partitioning.
5. **8.10 MergeJoinInner → 8.11 MergeJoinOuter** — sorted inputs, mark/restore.
6. **8.12 IndexNestedLoopJoin** — the parameterized index scan.
7. **8.13 EstimateEquiJoinRows → 8.14 BestJoinOrder** — the planner's side.
