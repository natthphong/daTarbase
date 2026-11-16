# Phase 9 — Transactions: MVCC, Isolation, Locks

How Postgres lets many transactions read and write the same table at once without readers
blocking writers: a 2-bit-per-xid commit log, wraparound-safe xid arithmetic, MVCC snapshots
and the `HeapTupleSatisfiesMVCC` visibility rules, UPDATE as "stamp xmax + append a new version",
READ COMMITTED vs REPEATABLE READ, write-write conflicts and EvalPlanQual, savepoints as
subtransactions, the heavyweight lock manager with deadlock detection, and finally VACUUM and
FREEZE that clean up after it all.

## Concepts

### Transaction ids and the commit log
- Every writing transaction gets a 32-bit **xid** (`TransactionId`). 0 = invalid, 1 = bootstrap,
  2 = frozen, normal xids start at 3. `GetNewTransactionId()` (varsup.c) hands them out in order.
- The fate of each xid lives in **pg_xact** (the "clog", clog.c): **2 bits per xid**, 4 per byte,
  32768 per 8 KB page — `in progress 00 / committed 01 / aborted 10 / sub-committed 11`.
  COMMIT is literally "flip two bits (after the WAL record is flushed)". ROLLBACK is "flip two bits"
  too — nothing in the table is undone.
- Xids **wrap around**. `TransactionIdPrecedes(a, b)` compares `int32(a - b) < 0`: each xid has
  2^31 xids in its past and 2^31 in its future. That is why old rows must be *frozen* before
  2^31 transactions pass, otherwise they suddenly look like they come from the future.

### Tuple headers
```
 t_xmin   inserting xid            t_cid     cmin / cmax (command id inside the txn)
 t_xmax   deleting/updating xid    t_ctid    (page,slot) of the next version, or itself
 t_infomask hint bits: XMIN_COMMITTED, XMIN_INVALID, XMAX_COMMITTED, XMAX_INVALID, (XMIN_FROZEN = both xmin bits)
```
- INSERT writes a version with `xmin = me`. DELETE only sets `xmax = me`. UPDATE = DELETE of the
  old version + INSERT of a new one, with `old.ctid → new` building a version chain.
- **Hint bits** cache clog lookups: the first reader that finds `xmin` committed sets
  `XMIN_COMMITTED` so later readers skip the clog. (In Postgres this dirties the page — the
  "why did my SELECT write to disk?" effect.)

### Snapshots and visibility
- `GetSnapshotData()` (procarray.c) records `xmin` (oldest running xid), `xmax` (next xid to be
  assigned) and `xip[]` (xids running right now, excluding my own). An xid is *in progress for
  this snapshot* if it is `>= xmax` or in `xip` — even if it has committed since.
- `HeapTupleSatisfiesMVCC()` (heapam_visibility.c): a version is visible iff its inserter is
  "committed for me" (or is me, in an earlier command) **and** its deleter is not.
  My own changes are filtered by **command id**: a statement never sees rows it wrote itself —
  this is what prevents the Halloween problem (`UPDATE t SET x = x + 1` touching new rows again).
- **READ COMMITTED** takes a new snapshot for every statement; **REPEATABLE READ** takes one at the
  transaction's *first statement* and keeps it (snapmgr.c `GetTransactionSnapshot`). RC therefore
  allows non-repeatable reads and phantoms; Postgres' RR (snapshot isolation) does not.

### Write-write conflicts
- Before stamping `xmax`, `heap_update()` asks `HeapTupleSatisfiesUpdate()` what state the row is
  in: `TM_Ok`, `TM_BeingModified` (another running txn has it — sleep on its xid lock),
  `TM_Updated` / `TM_Deleted` (a committed txn changed it), `TM_SelfModified`, `TM_Invisible`.
- After waiting, if the other transaction **aborted**, just retry. If it **committed**:
  RR raises `could not serialize access due to concurrent update`; RC runs **EvalPlanQual** —
  follow `ctid` to the newest version, re-check the WHERE clause on it and apply the update there.
  That is why under RC `UPDATE … SET v = v + 10` after a concurrent `v = v * 2` yields 210.

### Subtransactions (savepoints)
- `SAVEPOINT` starts a subtransaction with its **own xid** (parent recorded in pg_subtrans).
  Its writes are stamped with that xid. `ROLLBACK TO SAVEPOINT` marks those xids aborted in the clog
  — again no undo — and re-opens the savepoint with a fresh subxact. At top-level commit all live
  subxids are committed atomically with the parent (`TransactionIdCommitTree`).
- Others see a running txn's subxids as running too (`subxip[]` in the snapshot).

### Heavyweight locks
- Table locks have 8 modes with a fixed conflict matrix (lock.c `LockConflicts`): SELECT takes
  AccessShare, DML takes RowExclusive (writers don't block each other at table level), CREATE INDEX
  takes Share, DROP/TRUNCATE/most ALTERs take AccessExclusive.
- A request is granted only if it conflicts with no granted lock **and no earlier waiter** — strict
  FIFO prevents starving an AccessExclusive behind a stream of readers (the infamous
  "ALTER TABLE waits, then every SELECT queues behind it").
- `ProcLockWakeup()` grants waiters in queue order on release; `DeadLockCheck()` (deadlock.c)
  builds a wait-for graph with *hard* edges (blocked by a holder) and *soft* edges (queued behind
  a conflicting waiter) after `deadlock_timeout`, and aborts a transaction on the cycle.

### VACUUM and FREEZE
- Dead versions stay until VACUUM. A version is dead when its inserter aborted, or its deleter
  committed before **OldestXmin** — the oldest xmin of any snapshot still running
  (`HeapTupleSatisfiesVacuum`). A long-running transaction holds back the horizon and the table bloats.
- VACUUM also sets hint bits and, when `xmin` is older than the freeze cutoff, sets
  `HEAP_XMIN_FROZEN` so the row stays visible forever regardless of wraparound
  (`heap_prepare_freeze_tuple`). Aborted `xmax` values are cleared at the same time.

## Prerequisites
None — the phase is self-contained and only imports `pkg/types` (tests also use `pkg/testkit`).

## Suggested order
1. **9.1–9.3** clog bits and xid arithmetic.
2. **9.4–9.7** transaction manager and snapshots.
3. **9.8** `TupleVisible` — the heart of the phase; read every rule.
4. **9.9–9.12** heap operations and sequential scan (rollback / commit behaviour falls out).
5. **9.13–9.15** isolation levels, write-write conflicts, savepoints.
6. **9.16–9.19** lock manager and deadlock detection (independent of 9.8–9.15).
7. **9.20–9.21** VACUUM and FREEZE.
