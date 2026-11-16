package engine

import (
	"fmt"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	txn "github.com/natthphong/daTarbase/phase09_txn"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 11.2 — Visible [Easy]
//
// Decide whether a stored tuple version is visible to a snapshot by adapting its on-disk header to phase 9's TupleVisible.
//
// Phase 1's tuple header stores xmin and xmax; phase 9 decides visibility
// for a txn.Version that also has command ids (cmin / cmax) and hint bits.
// Our header has no command ids, so the engine uses one convention everywhere:
// every version counts as written by command 0 and every reader runs as
// command 1. Consequences:
//
//   - a transaction sees ALL of its own earlier inserts and none of its own
//     earlier deletes;
//   - a statement could see its own changes — UPDATE and DELETE avoid that
//     (the "Halloween problem") by collecting every target row before
//     changing anything (Problems 11.18, Txn.Delete).
//
// Rules:
//  1. Build v := txn.Version{Xmin: h.Xmin, Xmax: h.Xmax} (Cmin = Cmax = 0, no hint bits).
//  2. Evaluate it with a COPY of *snap whose CurCid is 1. Do not modify *snap.
//  3. Return txn.TupleVisible(v, &copy, clog, mine). The hint bits it sets on v
//     are thrown away (Postgres stores them in t_infomask and dirties the page;
//     we keep pages free of non-WAL-logged changes).
//
// Example (clog: 3 committed, 4 aborted, 5 running; snapshot {Xmin:5 Xmax:7 Xip:[5]}
// of transaction 6, mine = [6]):
//
//	{Xmin:3}         → true      {Xmin:4}         → false (aborted insert)
//	{Xmin:5}         → false     {Xmin:7}         → false (started after the snapshot)
//	{Xmin:3 Xmax:4}  → true      {Xmin:3 Xmax:5}  → true  (deleter still running)
//	{Xmin:3 Xmax:3}  → false     {Xmin:3 Xmax:7}  → true  (deleter committed after the snapshot)
//	{Xmin:6}         → true  (my own insert)
//	{Xmin:6 Xmax:6}  → false     {Xmin:3 Xmax:6}  → false (I deleted it)
//
// Constraints: O(log |Xip|), no allocation besides the snapshot copy.
// Postgres reference: HeapTupleSatisfiesMVCC() in src/backend/access/heap/heapam_visibility.c
func Visible(h tuple.TupleHeader, snap *txn.Snapshot, clog *txn.Clog, mine txn.CurrentXids) bool {
	panic("TODO: Problem 11.2")
}

// Problem 11.3 — Txn.Snapshot [Medium]
//
// Return the MVCC snapshot the transaction's next statement must use: a fresh one per statement under READ COMMITTED, one per transaction under REPEATABLE READ.
//
// Every statement (Query, Update, Delete) calls Snapshot exactly once and
// uses the result for all of its scans (GetTransactionSnapshot in Postgres).
//
// Rules:
//  1. t.done → (nil, ErrTxnClosed).
//  2. ReadCommitted: t.snap = t.db.mgr.GetSnapshot(t.xid) on EVERY call.
//  3. RepeatableRead: only when t.snap is nil (first statement — NOT Begin):
//     t.snap = t.db.mgr.GetSnapshot(t.xid). Later calls reuse t.snap.
//  4. Return a pointer to a COPY of *t.snap (a shallow struct copy), so callers
//     cannot change the stored snapshot. CurCid stays 0 (see Problem 11.2).
//  5. t.xid may still be 0 (no write yet): GetSnapshot(0) excludes nothing,
//     and a later xid is ≥ Xmax — it is still recognised as "mine" by Visible.
//
// Example (fresh database):
//
//	t1 := db.Begin(ReadCommitted); t1.assignXid()   // xid 3
//	t2 := db.Begin(ReadCommitted); t2.assignXid()   // xid 4
//	t2.Snapshot()                                   // {Xmin:3 Xmax:5 Xip:[3]}
//	rr := db.Begin(RepeatableRead)                  // t1 commits after this Begin …
//	rr.Snapshot()                                   // … but before the first statement: Xip [4], t1 visible
//	                                                // t2 commits now; rr.Snapshot() still has Xip [4]
//
// Constraints: one GetSnapshot per call under RC, one per transaction under RR.
// Postgres reference: GetTransactionSnapshot() in src/backend/utils/time/snapmgr.c
func (t *Txn) Snapshot() (*txn.Snapshot, error) {
	panic("TODO: Problem 11.3")
}

// Problem 11.6 — Txn.Commit [Medium]
//
// Make the transaction's changes durable and visible: WAL commit record, flush, then the clog bit.
//
// A commit changes nothing in the table: the inserted versions already carry
// xmin = my xid, deleted ones xmax = my xid. What flips is two bits in the
// clog. Durability comes from the WAL: the COMMIT record must be on disk
// before anyone may learn that the transaction committed.
//
//	wal.Append(COMMIT) ─► wal.Flush (fsync) ─► clog: committed ─► leave the running set
//
// Rules:
//  1. t.done → ErrTxnClosed.
//  2. Read-only transaction (t.xid == 0): just t.finish() — no WAL record, no
//     flush, no clog change.
//  3. lsn := t.db.wal.Append(t.xid, wal.RecCommit, nil), then
//     t.db.wal.Flush(lsn). If the flush fails, return the error and leave the
//     transaction open (Postgres PANICs here; nothing was promised yet).
//  4. Only then t.db.mgr.Commit(t.xid) (clog → committed, removed from the
//     running set), and t.finish().
//
// Example:
//
//	tx inserts 3 rows (xid 3) → WAL: 3 × UPDATE(heap insert), nothing flushed yet
//	tx.Commit()  → WAL += COMMIT@x (txn 3); wal.Flushes +1; clog[3] = committed
//	tx.Commit()  → ErrTxnClosed
//	read-only tx.Commit() → nil, WAL unchanged, Flushes unchanged
//
// Constraints: exactly one WAL flush per writing transaction.
// Postgres reference: RecordTransactionCommit() in src/backend/access/transam/xact.c
func (t *Txn) Commit() error {
	panic("TODO: Problem 11.6")
}

// Problem 11.7 — Txn.Rollback [Easy]
//
// Abort the transaction: mark its xid aborted and leave every page untouched.
//
// Postgres does not undo anything at rollback. The versions the transaction
// inserted stay on their pages with xmin = an aborted xid — invisible to
// everyone (Problem 11.2) until VACUUM removes them; versions it deleted keep
// xmax = an aborted xid, which readers ignore.
//
// Rules:
//  1. t.done → ErrTxnClosed.
//  2. If t.xid != 0: t.db.wal.Append(t.xid, wal.RecAbort, nil) — do NOT flush
//     it (losing an abort record in a crash is harmless: a transaction without
//     a commit record is treated as aborted) — then t.db.mgr.Abort(t.xid).
//  3. t.finish(). No page, index, FSM or VM is touched.
//
// Example:
//
//	tx inserts (1,'a',10,1) at (0,1) with xid 3; tx.Rollback()
//	→ clog[3] = aborted; WAL += ABORT (txn 3); wal.Flushes unchanged;
//	  the tuple bytes at (0,1) still say xmin = 3; a new scan returns nothing
//
// Constraints: O(1), no I/O.
// Postgres reference: RecordTransactionAbort() in src/backend/access/transam/xact.c
func (t *Txn) Rollback() error {
	panic("TODO: Problem 11.7")
}

// Problem 11.10 — Txn.Insert [Medium]
//
// INSERT one row: coerce it to the table's types, store it in the heap, and add an entry to every index of the table.
//
// Rules:
//  1. t.done → ErrTxnClosed. Unknown table → an error wrapping ErrNoSuchTable.
//  2. r, err := coerceRow(schema, row) (provided): int8 literals become int4
//     where the column is int4, etc. Errors are returned as-is (wrapping
//     tuple.ErrArity, tuple.ErrTypeMismatch or ErrOutOfRange). A failed
//     coercion must not assign an xid.
//  3. xid := t.assignXid(); tid, err := heap.Insert(xid, r) (Problem 11.4).
//  4. Then, for every index of the table in catalog order (t.db.indexesOf):
//     item := ix.Entry(r, tid) (Problem 11.8) and ix.Tree.Insert(item).
//     The heap tuple must exist before an index entry points at it.
//  5. Return tid.
//
// Example (emp(id int4, name text, dept int4), index emp_dept on (dept)):
//
//	tx.Insert("emp", R(1, "alice", 10))     → (0,1); emp_dept gains key EncodeKey((10)) → (0,1)
//	tx.Insert("emp", R(2, "bob"))           → error wrapping tuple.ErrArity, tx.XID() still 0
//	tx.Insert("emp", R(3000000000, "x", 1)) → error wrapping ErrOutOfRange
//	tx.Insert("nope", R(1))                 → error wrapping ErrNoSuchTable
//
// Constraints: one heap insert and one B+Tree insert per index.
// Postgres reference: ExecInsert() in src/backend/executor/nodeModifyTable.c,
// ExecInsertIndexTuples() in execIndexing.c
func (t *Txn) Insert(table string, row types.Row) (types.TID, error) {
	panic("TODO: Problem 11.10")
}

// Query runs a query as one statement and returns all its rows.
func (t *Txn) Query(q *Query) ([]types.Row, error) {
	p, err := t.PlanQuery(q)
	if err != nil {
		return nil, err
	}
	return exec.Collect(p.Iter)
}

// targets runs the statement's scan of a DML query and returns every target
// row (with its ctid as the last column), BEFORE anything is modified.
func (t *Txn) targets(q *Query) (*TableHeap, []types.Row, error) {
	if t.done {
		return nil, nil, ErrTxnClosed
	}
	if !q.isPlainScan() {
		return nil, nil, fmt.Errorf("%w: UPDATE / DELETE take Scan(table).Where(...) only", ErrBadQuery)
	}
	h := t.db.heaps[q.table]
	if h == nil {
		return nil, nil, fmt.Errorf("%w: %q", ErrNoSuchTable, q.table)
	}
	snap, err := t.Snapshot()
	if err != nil {
		return nil, nil, err
	}
	p, err := t.PlanScan(snap, ScanRequest{Table: q.table, Conds: q.conds, EmitTID: true})
	if err != nil {
		return nil, nil, err
	}
	rows, err := exec.Collect(p.Iter)
	return h, rows, err
}

// Delete runs DELETE FROM table WHERE ...: every visible matching row gets
// xmax = my xid (Problem 11.17). Index entries stay until VACUUM.
// Returns the number of rows deleted.
func (t *Txn) Delete(q *Query) (int, error) {
	h, rows, err := t.targets(q)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if err := h.Delete(t, valueTID(r[len(r)-1])); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Problem 11.18 — Txn.Update [Medium]
//
// UPDATE table SET col = value, ... WHERE ...: for every target row, delete the old version and insert the new one.
//
// MVCC never overwrites a row. UPDATE = DELETE of the old version (xmax = me)
// + INSERT of a new version at a new TID, and every index gets an entry for
// the new TID (we have no HOT: even an update that does not touch indexed
// columns adds index entries).
//
// Rules:
//  1. Validate everything before changing anything: t.done → ErrTxnClosed;
//     q not a plain Scan(table).Where(...) (q.isPlainScan() false) → ErrBadQuery;
//     unknown table → wraps ErrNoSuchTable; every key of set must name a column
//     ("col" or "table.col", see resolve over qualified names) → wraps
//     ErrNoSuchColumn; every value toValue'd then coerceValue'd to the column
//     kind → its error. Look at the set keys in sorted order.
//  2. snap := t.Snapshot(); plan := t.PlanScan(snap, ScanRequest{Table, Conds:
//     q.conds, EmitTID: true}); collect ALL target rows (exec.Collect) before
//     modifying anything — otherwise the scan could meet the new versions and
//     update them again (the Halloween problem). The provided t.targets(q)
//     does exactly this step (and repeats the checks of rule 1 for the query).
//  3. For each target, in scan order: tid := valueTID(last column); the new
//     row is a copy of the other columns with the assignments applied;
//     h.Delete(t, tid) (Problem 11.17), then t.Insert(table, newRow)
//     (Problem 11.10). On the first error return (rows updated so far, err) —
//     the caller is expected to roll back, as Postgres aborts the transaction.
//  4. Return the number of rows updated.
//
// Example (emp: (1,'alice',10) (2,'bob',20) (3,'carol',10), index on dept):
//
//	tx.Update(Scan("emp").Where("dept", "=", 10), map[string]any{"dept": 30}) → 2
//	  versions at (0,1) and (0,3) get xmax = tx; new versions (1,'alice',30), (3,'carol',30)
//	  at (0,4), (0,5); emp_dept now holds 5 entries
//	the same tx: Query(Scan("emp").Where("dept", "=", 10)) → no rows; dept 30 → 2 rows
//	another transaction updating alice before tx commits → ErrWriteConflict
//
// Constraints: one scan; targets materialised once.
// Postgres reference: ExecUpdate() in src/backend/executor/nodeModifyTable.c, heap_update() in heapam.c
func (t *Txn) Update(q *Query, set map[string]any) (int, error) {
	panic("TODO: Problem 11.18")
}
