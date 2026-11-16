# Phase 11 — Engine: one database out of ten phases

The capstone: glue phases 1–10 into a small but real Postgres-like database with a Go API — tables stored as files of 8 KB slotted pages behind buffer pools, rows encoded as heap tuples with `xmin`/`xmax`, MVCC snapshots and a commit log, B+Tree indexes with INCLUDE columns, a planner that picks Seq / Index / Index Only scans and hash joins and stacks sort, aggregation and limit operators on top, UPDATE / DELETE as new tuple versions, VACUUM, checkpoints, and crash recovery that replays the write-ahead log. Every problem is a piece of glue: the place where you decide how two subsystems meet.

```go
db, _ := engine.Open(dir)
db.CreateTable("emp", types.NewSchema("id", types.KindInt32, "name", types.KindText, "dept", types.KindInt32))
db.CreateIndex("emp_dept", "emp", []string{"dept"}, engine.Include("name"))
tx := db.Begin(engine.ReadCommitted)
tx.Insert("emp", testkit.R(1, "alice", 10))
rows, _ := tx.Query(engine.Scan("emp").Where("dept", "=", 10).
	Join(engine.Scan("dept"), engine.LeftJoin, "dept", "dept.id").OrderBy("name").Limit(10))
tx.Commit()
fmt.Println(db.Explain(engine.Scan("emp").Where("dept", "=", 10).Select("name")))
db.Close()
```

Run the narrated demo with `go run ./cmd/demo`.

## Concepts

### 1. The architecture you just assembled

```
            Txn.Query / Insert / Update / Delete              DB.Vacuum   DB.Checkpoint   Open → Recover
                         │
                 PlanQuery (11.16) ──► PlanScan (11.14) / PlanJoin (11.15)
                         │                  │
   phase 6 Filter/Limit/Project   phase 7 Sort/TopN/HashAggregate   phase 8 hash & nested-loop joins
                         │
      SeqScan (11.5) ── IndexScan (11.12) ── IndexOnlyScan (11.13)      Visible (11.2) ◄── phase 9 MVCC + clog
             │                 │                                                ▲
             │           phase 5 B+Tree  ◄── keys: phase 1 EncodeKey            │ xmin / xmax
             ▼                                                                  │
      TableHeap (11.4, 11.17) ── phase 4 FSM ── phase 1 FormTuple / DeformTuple ─┘
             │
      phase 3 BufferPool (clock) ──► walDisk ──► phase 3 FileDisk  base/<relid>
                                        │  write-ahead rule: flush WAL up to the page LSN first
                                        ▼
                               pg_wal  (phase 10 record format)        pg_xact (phase 9 clog pages)
```

This is the same layering as Postgres: the executor (`execMain.c`) pulls tuples from
access methods (`heapam.c`, `nbtree`), which work on pages in shared buffers
(`bufmgr.c`), which reach the storage manager (`smgr`/`md.c`) only after `XLogFlush`.

### 2. Data directory

| Postgres | here | what |
|---|---|---|
| `global/pg_control`, `pg_class`, `pg_attribute`, `pg_index` | `catalog.json` | table and index definitions, replaced atomically (`durable_rename`) |
| `base/<db>/<relfilenode>` | `base/<relid>` | one heap file per table, 8 KB pages; relids start at 16384 (`FirstNormalObjectId`) |
| `pg_wal/0000000100000000000000xx` | `pg_wal` | one growing WAL file |
| `pg_xact/0000` | `pg_xact` | 2 bits per xid, saved at every checkpoint |
| index relfilenodes | — | our B+Trees live in memory and are rebuilt from the heap by `Open` (like a `REINDEX` at startup) |
| `_fsm`, `_vm` forks | — | free space map and visibility map are in memory; rebuilt / empty after `Open` |

### 3. A heap page here

Phase 2's page header has no `pd_lsn`, so every heap page keeps its LSN in an 8-byte item
in slot 0. User tuples start at slot 1 — exactly as Postgres's line pointers start at 1.

```
| lower | upper | flags | checksum | lp0 | lp1 | lp2 | … free … | tuple 2 | tuple 1 | LSN (8 B) |
                                    └─ slot 0 = page LSN          tuples: phase-1 format
```

The buffer pool hands out raw frames; `pageView` reinterprets a frame as a `*page.Page`
without copying (in Postgres a `Page` is just a pointer into a shared buffer). Checksums
are verified when a page is read from disk and stamped into a copy when it is written
(`PageIsVerifiedExtended`, `PageSetChecksumCopy`) — not on every access.

### 4. MVCC end to end

- **INSERT** (`heap_insert`): `FormTuple(row, xmin = my xid)` → FSM → page → WAL record →
  page LSN. Nothing is visible to others until the commit bit flips.
- **DELETE** (`heap_delete`): write `xmax = my xid` into the stored header. Before that,
  `HeapTupleSatisfiesUpdate` logic: a running deleter is a write–write conflict (Postgres
  waits on its xid lock; we fail fast with `ErrWriteConflict`), a committed one under
  REPEATABLE READ is `could not serialize access due to concurrent update`.
- **UPDATE** = DELETE + INSERT of a new version at a new TID. Without HOT every index
  gets a new entry, even when no indexed column changed — the write amplification that
  HOT (heap-only tuples) and Postgres 16's "HOT for BRIN" exist to avoid.
- **Halloween problem**: a statement must not see its own changes. Postgres uses command
  ids (`cmin`/`cmax`); our header has none, so UPDATE/DELETE collect all target TIDs before
  modifying anything.
- **Lazy xids**: like Postgres, a transaction gets an xid only at its first write; a
  read-only COMMIT writes no WAL at all.
- **Snapshots**: READ COMMITTED takes one per statement, REPEATABLE READ one at the first
  statement (`GetTransactionSnapshot`). Indexes have no visibility information, so every
  index entry costs a heap fetch to recheck — except on pages the **visibility map** marks
  all-visible, which is what makes an **Index Only Scan** cheap. VACUUM sets those bits.

### 5. Commit, rollback, durability

- COMMIT = append a commit record, `XLogFlush` it (fsync), then set the clog bits
  (`RecordTransactionCommit`). The data pages are written much later — **no-force**.
- ROLLBACK changes **nothing** in the table: it marks the xid aborted. Its tuples stay until
  VACUUM; readers skip them because the clog says aborted.
- The buffer pool may evict a page with uncommitted changes — **steal**. The *write-ahead
  rule* makes that safe: `walDisk.WritePage` flushes the WAL up to the page's LSN first
  (`FlushBuffer` → `XLogFlush(BufferGetLSN)`).

### 6. Crash recovery (Postgres-style redo, no undo)

```
 pg_wal:  …… [CHECKPOINT] rec rec rec COMMIT rec … rec [torn tail]
                         └──────────── the tail: redo all of it ─────┘
```

- A **checkpoint** flushes the WAL, writes every dirty page, saves `pg_xact`, and only then
  writes the CHECKPOINT record (`CreateCheckPoint`). Everything before it is in the data files.
- **Redo** walks the tail: a heap record is applied only if its page's LSN is older than the
  record (`XLogReadBufferForRedo` → `BLK_NEEDS_REDO` / `BLK_DONE`), which makes replay
  idempotent. Inserts are deterministic: the page is in exactly the state it had before
  the original insert, so `InsertTuple` lands in the logged slot again.
- No **undo**: changes of transactions without a commit record are replayed too and simply
  stay invisible — every xid still "in progress" after redo is marked aborted. (Phase 10
  implements full ARIES undo with CLRs; Postgres doesn't need it thanks to MVCC.)
- An **end-of-recovery checkpoint** makes the next start fast.
- Simplifications vs Postgres: no full-page images (we never tear a page write), one WAL
  file instead of 16 MB segments, `pg_xact` saved at checkpoints instead of being an SLRU
  that is itself protected by the WAL.

### 7. Planning (rule-based, Postgres-shaped output)

- Access path: a condition `col op const` on the **leading** column of a B+Tree can become a
  key range (`match_clause_to_indexcol`). Memcomparable keys turn "leading column = v" into
  a key prefix range `[enc(v), succ(enc(v)))`; NULLs sort last, so ranges stop before them.
  Equality beats a range; `<>` and `= NULL` never use an index.
- Index Only Scan when the index (key + INCLUDE columns) covers every column the query
  reads. Postgres decides with costs (`cost_index`, the visibility-map fraction); we use
  rules so EXPLAIN is predictable.
- Joins: every join here is an equi-join → hash join (build = right input, the "Hash" node);
  SEMI / ANTI use a nested loop that stops at the first match.
- `ORDER BY … LIMIT n` uses a bounded top-N heapsort (`tuplesort_set_bound`).
- `db.Explain` prints the plan like `EXPLAIN (COSTS OFF)`.

### 8. VACUUM

A version is dead when its inserter aborted, or its deleter committed before the
**horizon** (`GetOldestNonRemovableTransactionId`: the oldest xid any running transaction or
REPEATABLE READ snapshot might still consider running). VACUUM removes the dead versions'
index entries **first** (`lazy_vacuum_all_indexes`), then frees their line pointers and
compacts the page (`heap_page_prune`), WAL-logs it, updates the FSM, and sets the
all-visible bit when every remaining tuple is visible to everyone. A long-running
REPEATABLE READ transaction holds the horizon back — the classic cause of table bloat.

## Prerequisites

This phase imports every other phase: 1 (tuples, key encoding), 2 (slotted pages),
3 (file disk, buffer pool), 4 (free space map), 5 (B+Tree), 6 (Filter / Limit / Project,
expressions), 7 (Sort, TopN, HashAggregate), 8 (hash and nested-loop joins),
9 (transaction manager, clog, `TupleVisible`) and 10 (WAL record format, `ReadRecords`).
Solve those first — the engine's tests run their code.

## Suggested order

1. **11.1** durable catalog writes.
2. **11.2–11.3** visibility adapter and snapshots.
3. **11.4–11.5** heap insert and the sequential scan.
4. **11.6–11.7** commit and rollback.
5. **11.8–11.13** indexes: entries, CREATE INDEX, maintenance on insert, key ranges, index scans, index-only scans.
6. **11.14–11.16** the planner: access paths, joins, the whole query.
7. **11.17–11.18** DELETE and UPDATE with conflict detection.
8. **11.19** VACUUM.
9. **11.20–11.21** checkpoint and crash recovery — the final exam (`TestP11_21_EndToEnd`).

Run one problem's tests with `go test -run '^TestP11_4_' ./phase11_engine/`.
