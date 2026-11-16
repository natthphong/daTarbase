# Phase 6 — Executor: Expressions, Scans, Planner basics

The streaming half of the executor and the first planner decisions: an expression evaluator with SQL three-valued logic, the pipelined Filter / Projection / Limit operators, every single-table access path Postgres has (Seq Scan, Index Scan, Index Only Scan with the visibility map, Bitmap Index Scan + BitmapAnd/BitmapOr + Bitmap Heap Scan with lossy pages), and the planner pieces that choose between them: selectivity estimation from `pg_statistic`, the `costsize.c` cost model, EXPLAIN output and constant folding. Everything runs against small instrumented in-memory fakes (heap table, B-tree index, visibility map) so the tests can count page reads and heap fetches.

## Concepts

### The Volcano model
Every plan node implements `Open / Next / Close` (`pkg/exec.Iterator`; Postgres:
`ExecInitNode / ExecProcNode / ExecEndNode`). The root pulls one row at a time; each
node pulls from its children only when it needs a row. Two consequences that the tests
check with counters:

- **Limit stops work below it.** `SELECT * FROM big LIMIT 1` reads one heap page:
  once `ExecLimit` has returned its rows it never calls its child again
  (`TestP6_5_SeqScan_Lazy`).
- **Pipelining.** Filter and Projection hold no rows; they transform one row per call.
  In Postgres they are not even separate nodes — the qual (`ExecQual`) and the target
  list (`ExecProject`) are attached to the scan or join node. EXPLAIN shows them as
  `Filter:` lines and the output column list.

### Expressions and three-valued logic
Postgres compiles expression trees into a flat program of steps (`ExecInitExpr` →
`ExecInterpExpr`, optionally JIT-compiled with LLVM). We interpret the tree directly,
but the semantics are the same:

```
 AND | T F N     OR | T F N      NOT: T→F  F→T  N→N
 ----+------     ---+------
  T  | T F N      T | T T T      comparisons, + - * /, IN: NULL in → NULL out ("strict")
  F  | F F F      F | T F N      IS [NOT] NULL: never NULL
  N  | N F N      N | T N N
```

- `WHERE` keeps a row only if the qual is **TRUE**; NULL rejects it. That's why
  `WHERE x <> 5` silently drops rows where `x IS NULL`, and why `NOT IN (…, NULL)`
  returns nothing.
- Postgres evaluates `AND`/`OR` arguments left to right and stops early
  (`EEOP_BOOL_AND_STEP` jumps to the end on FALSE), but the SQL standard does not
  promise an order — the planner may reorder quals by cost. Never rely on
  `x <> 0 AND 1/x > 2` being safe; use `CASE`.
- Integer operators check overflow (`int4pl` → `ERROR: integer out of range`) using
  `pg_add_s32_overflow`; division checks for zero and for `INT_MIN / -1`. Float
  division by zero is an error too; float overflow on `+ - *` is also an error in
  Postgres (we let it become `Inf`).

### Access paths

```
Seq Scan            heap pages 0..N-1 in order               sequential I/O, reads everything
Index Scan          btree range → heap fetch per TID         random I/O, rows in index order
Index Only Scan     btree range → heap only if page not      "Heap Fetches: N" in EXPLAIN ANALYZE
                    all-visible in the visibility map
Bitmap Index Scan   btree range → TID bitmap (no heap)       can be AND/OR-combined across indexes
Bitmap Heap Scan    bitmap pages in physical order           one read per page, recheck if lossy
```

- **Index Scan** (`nodeIndexscan.c`, `_bt_first` + `_bt_next`): one descent to the
  first key, then walk leaves sideways. Each entry is a TID; the heap fetch is a random
  page read unless the heap happens to be stored in index order (*correlation*). Index
  entries of deleted rows stay until VACUUM, so the scan fetches dead tuples and throws
  them away (`TestP6_6_IndexScan_DeadTuplesAndDone`).
- **Index Only Scan** (`nodeIndexonlyscan.c`): the index tuple has the column values but
  no MVCC information (xmin/xmax live in the heap tuple). The **visibility map** (the
  `_vm` fork, 2 bits per heap page: all-visible, all-frozen) says "every tuple on this
  page is visible to every transaction". If the bit is set the heap is skipped; any
  write to the page clears the bit, and VACUUM sets it again. A table that was never
  vacuumed gets an index-only scan that fetches every row — check `Heap Fetches` in
  `EXPLAIN (ANALYZE)`.
- **Bitmap scans** (`nodeBitmapIndexscan.c`, `nodeBitmapHeapscan.c`, `tidbitmap.c`):
  a Bitmap Index Scan only collects TIDs into a hash table keyed by page
  (`PagetableEntry`, one bit per tuple). Several bitmaps combine with `BitmapAnd` /
  `BitmapOr` — this is how Postgres uses *two different indexes* for one table
  (`WHERE a = 1 OR b = 2`). The Bitmap Heap Scan then visits pages in block order: each
  page read once, mostly sequentially, and prefetched (`effective_io_concurrency`).
  Output is in physical order, not index order.
- **Lossy bitmaps.** A bitmap that would outgrow `work_mem` is *lossified*
  (`tbm_lossify`): exact page entries are replaced by "chunk" entries that only say
  "something on this page may match". Every tuple on a lossy page has to be rechecked
  against the original condition — EXPLAIN shows `Recheck Cond:` always, and EXPLAIN
  ANALYZE shows `Heap Blocks: exact=… lossy=…` and `Rows Removed by Index Recheck`.
  Intersecting a lossy page with an exact one keeps the exact slots but marks them
  for recheck (`tbm_intersect_page`).

### Selectivity: guessing how many rows match
`ANALYZE` samples 300 × `default_statistics_target` rows and stores per column
(`pg_stats`): `null_frac`, `n_distinct` (negative = ratio of rows, so it scales with
table growth), `most_common_vals` + `most_common_freqs`, `histogram_bounds`
(equi-depth over the values *not* in the MCV list) and `correlation`.

```
          null_frac │      MCVs (exact)        │  rest, described by the histogram
row population:  10%│ 5: 20%   7: 10%           │ 60%:  0 ── 10 ── 20 ── 30 ── 40
                                                         15% each bucket
```

- `col = c` (`var_eq_const`): MCV hit → its frequency; else spread the rest evenly over
  the remaining distinct values, capped by the least common MCV.
- `col < c` (`scalarineqsel`): exact sum over matching MCVs + histogram fraction below
  `c` (linear interpolation inside the bucket, via `convert_to_scalar`) × the non-MCV,
  non-NULL population.
- `col BETWEEN a AND b`: the two inequalities are *not* multiplied as independent
  clauses; `clauselist_selectivity` recognises the range pair and computes
  `s(≤ b) + s(≥ a) − 1` (+ null_frac, which both sides excluded).
- Defaults when there are no stats: `= → 0.005`, `< → 1/3`, range `→ 0.005`.
  Multi-column correlations are invisible unless you `CREATE STATISTICS`.

### The cost model
Costs are in arbitrary units where one sequential page read = 1.0 (`seq_page_cost`).
`random_page_cost = 4` (lower it to ~1.1 on SSDs), `cpu_tuple_cost = 0.01`,
`cpu_index_tuple_cost = 0.005`, `cpu_operator_cost = 0.0025`.

- **Seq Scan** = pages + tuples × (cpu_tuple + cpu_operator per qual).
- **Index Scan** (`cost_index`) interpolates heap I/O between the uncorrelated case
  (every fetched page is random, counted with the Mackert–Lohman formula
  `index_pages_fetched`) and the perfectly correlated case (one random read then
  sequential), weighted by `correlation²`.
- **Bitmap Heap Scan** (`cost_bitmap_heap_scan`) pays per page between
  `random_page_cost` and `seq_page_cost`, sliding toward sequential as the fraction of
  the table read grows (`sqrt(pages/total)`), plus recheck CPU per tuple.
- Typical outcome (and the crossovers in `TestP6_14_*`): one row → Index Scan;
  a handful to tens of percent of an uncorrelated column → Bitmap Heap Scan; large
  fractions → Seq Scan; a well-correlated column keeps the Index Scan up to much larger
  fractions.
- Real Postgres adds startup costs, btree descent CPU, caching (`effective_cache_size`),
  parallel workers and `enable_*` switches; our model keeps the core formulas.

### EXPLAIN
`ExplainNode` prints each node as `Name [using idx] on rel  (cost=startup..total rows=N width=W)`,
children prefixed by `->  ` and indented 6 more spaces per level, with qual lines
(`Index Cond`, `Recheck Cond`, `Filter`) under their node. `startup` is the cost before
the first row (high for Sort, ~0 for Seq Scan) — that is what a `LIMIT` cares about.

### Constant folding
`eval_const_expressions` runs over every expression during planning: `2 + 3` becomes
`5` (so `id < 2 + 3` can be an index condition), `x AND true` becomes `x`, strict
functions of a NULL constant become NULL, and an always-false `WHERE` becomes a
`One-Time Filter: false` that skips the scan entirely. Because folding runs at plan
time, `SELECT … WHERE x > 1/0` fails even on an empty table.

## Prerequisites
None — self-contained. Uses only `pkg/types`, `pkg/exec` and `pkg/testkit`.

## Suggested order
1. **6.1 Eval** — everything else evaluates expressions with it.
2. **6.2–6.4** Filter, Project, Limit — the pipelined operators.
3. **6.5–6.7** Seq Scan, Index Scan, Index Only Scan — the access paths, with I/O counters.
4. **6.8–6.10** Bitmap Index Scan, BitmapAnd/Or, Bitmap Heap Scan.
5. **6.11–6.13** selectivity: `=`, `<`, `BETWEEN`.
6. **6.14 ChooseScan** — the cost model; **6.15 Explain**; **6.16 FoldConstants**.
