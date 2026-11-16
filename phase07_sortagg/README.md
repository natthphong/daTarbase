# Phase 7 — Sorting & Aggregation

The executor operators that cannot simply stream rows through: ORDER BY (in-memory stable sort, top-N heapsort for `ORDER BY … LIMIT`, external merge sort that spills to temp files, incremental sort), GROUP BY (hash aggregation and streaming sorted aggregation with COUNT/SUM/AVG/MIN/MAX, DISTINCT aggregates and HAVING), SELECT DISTINCT (hash and Unique), and window functions (ROW_NUMBER, RANK, DENSE_RANK, running SUM). They all use the Volcano `exec.Iterator` interface from `pkg/exec` and one shared comparator.

## Concepts

### One comparator for every sort: SortSupport
- Each `ORDER BY` item becomes a `SortSupportData` with a comparator function,
  `ssup_reverse` (DESC) and `ssup_nulls_first`. `ApplySortComparator()` handles NULLs
  *before* calling the type's comparator — NULL ordering is not a property of the type.
- Defaults: `ASC` ⇒ `NULLS LAST`, `DESC` ⇒ `NULLS FIRST`. That is because Postgres
  treats NULL as "larger than any value"; flipping the direction flips where NULLs go.
  `NULLS FIRST/LAST` written explicitly overrides this, independent of direction.
- Text comparison normally goes through the collation (`strcoll`/ICU). This project
  compares bytes, i.e. `COLLATE "C"`.

### tuplesort: one module, several strategies
`src/backend/utils/sort/tuplesort.c` is used by Sort nodes, CREATE INDEX, merge joins,
window functions and ordered aggregates. Its state machine:

```
TSS_INITIAL ──(fits in work_mem)────────────────► quicksort in memory   "Sort Method: quicksort"
     │
     ├──(LIMIT N known, > 2N tuples arrive)──► TSS_BOUNDED heap      "Sort Method: top-N heapsort"
     │
     └──(exceeds work_mem)──► TSS_BUILDRUNS ──► dump runs to tapes ──► TSS_FINALMERGE
                                                                     "Sort Method: external merge  Disk: …kB"
```

- **Top-N heapsort.** When a Limit sits above the Sort (`ExecSetTupleBound`),
  tuplesort keeps a max-heap of the best N tuples ("reversed" heap: worst at the root).
  Each new tuple is compared with the root and either discarded or replaces it — memory
  O(N), time O(n log N). The whole input is still read.
- **Run generation.** When memory fills up, the in-memory tuples are quicksorted and
  written out as one sorted run (`dumptuples`). Old Postgres used *replacement
  selection* (runs ≈ 2×work_mem on random input); 9.6 restricted it and 11 removed it —
  quicksorting each memory load is far more cache friendly.
- **Merging.** Runs live on "logical tapes" (`logtape.c`) inside one temp file in
  `base/pgsql_tmp`, blocks are recycled as they are read so disk usage stays ~1× the data.
  The merge keeps a heap of the current head tuple of each tape (`mergeonerun`), so it
  needs one buffer per input tape. `tuplesort_merge_order()` derives the max fan-in from
  work_mem; with more runs than that, Postgres merges in several passes (balanced k-way
  merge since PG 15, polyphase before). The last pass is done *on the fly* while
  the Sort node returns tuples — it is never written out (we write it, to keep the code simple).
- **Stability.** SQL does not promise a stable sort, and Postgres' quicksort is not
  stable. This project insists on stability so test results are deterministic; the
  external merge keeps it by breaking ties toward the lower run number.
- **Incremental sort (PG 13).** If the input is already sorted on a prefix `a` of
  `ORDER BY a, b` (e.g. from an index), `nodeIncrementalSort.c` sorts each group of
  equal `a` separately: lower memory, and the first rows appear without reading the whole
  input — great under a LIMIT. It switches between "full sort" and "prefix sort" modes
  depending on group sizes.

### Aggregation: transition state + final function
- Every aggregate is `(transfn, finalfn, initcond)` in `pg_aggregate`. `count(*)` has
  transition `int8inc`; `avg(int4)` keeps `{count, sum}` and the final function divides.
  `sum`/`avg` skip NULL inputs because their transition functions are *strict*: a strict
  transfn is never called with NULL. So `SUM` of nothing is NULL, while `COUNT` starts at 0.
- Result types: `sum(int4)` → int8, `sum(int8)` → numeric, `avg(int)` → numeric,
  `avg(float8)` → float8. We simplify: int sum → int8, anything with a float → float8,
  AVG always float8.
- **DISTINCT aggregates** (`count(DISTINCT x)`): Postgres sorts each group's inputs and
  skips adjacent duplicates (`process_ordered_aggregate_single`). We use a per-group hash
  set instead — same result, different memory profile.
- **HashAgg vs GroupAgg.** `nodeAgg.c` has strategies `AGG_PLAIN` (no GROUP BY: always
  exactly one output row, even for empty input), `AGG_SORTED` (input sorted on the group
  keys, one group's state in memory, output streams), and `AGG_HASHED` (a hash table of
  group → transition states; since PG 13 it spills partitions to disk when it exceeds
  `work_mem × hash_mem_multiplier`). Grouping uses "not distinct" equality, so all NULLs
  form a single group.
- **HAVING** is just a qual evaluated on the finished group row, after the final functions.

### DISTINCT
- `SELECT DISTINCT` is planned either as a HashAggregate with no aggregates (streams
  first-seen rows) or Sort + **Unique** (`nodeUnique.c`), which only compares each row
  with the previous one — O(1) memory, but correct only on sorted input.

### Window functions
- `nodeWindowAgg.c` receives rows sorted by `PARTITION BY` then `ORDER BY` (the planner
  adds a Sort), buffers one partition in a tuplestore and tracks *peer groups* — rows
  equal on every ORDER BY key.
- `row_number` counts rows; `rank` = position of the first peer (gaps after ties);
  `dense_rank` counts peer groups (no gaps).
- Aggregates used as window functions use the frame. The default frame is
  `RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW`, and in RANGE mode "current row"
  means *the current row's last peer* — so tied rows get the same running sum. Without
  ORDER BY, every row is a peer of every other, and the sum is the partition total.

## Prerequisites
None beyond the provided `pkg/types`, `pkg/exec` and `pkg/testkit`. The phase is
self-contained.

## Suggested order
1. **7.1 CompareRows** — everything else uses it.
2. **7.2 Sort**, **7.3 TopN** — in-memory sorting.
3. **7.4 GenerateRuns → 7.5 MergeRuns → 7.6 MergeAll → 7.7 ExternalSort** — external merge sort
   using the provided run-file store (`RunStore`, `WriteRow`/`ReadRow`).
4. **7.8 Advance → 7.9 Finalize → 7.10 HashAggregate → 7.11 GroupAggregate** — aggregation.
5. **7.12 HashDistinct**, **7.13 Unique**.
6. **7.14 IncrementalSort**.
7. **7.15 Window**.
