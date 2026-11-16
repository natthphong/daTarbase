package txn

import (
	"errors"

	"github.com/natthphong/daTarbase/pkg/types"
)

// IsolationLevel of a transaction.
type IsolationLevel uint8

const (
	ReadCommitted  IsolationLevel = iota // a new snapshot for every statement (Postgres default)
	RepeatableRead                       // one snapshot, taken at the first statement
)

var (
	// ErrSerialization is what Postgres raises under REPEATABLE READ when a row
	// it wants to modify was changed by a transaction it cannot see.
	ErrSerialization = errors.New("could not serialize access due to concurrent update")
	// ErrWouldBlock: the row is being modified by a transaction that is still
	// running after DB.Wait returned (or DB.Wait is nil).
	ErrWouldBlock = errors.New("txn: row is locked by a running transaction")
	// ErrNoSavepoint: ROLLBACK TO / RELEASE of a savepoint that does not exist.
	ErrNoSavepoint = errors.New("txn: savepoint does not exist")
)

// DB ties the transaction manager to one heap table.
type DB struct {
	Mgr  *TxnManager // xids, clog, running set
	Heap *Heap       // the one table
	// Wait simulates blocking on a row lock held by transaction xid (the xmax of
	// the row). A real backend sleeps until xid ends; tests install a hook that
	// commits or aborts xid. If Wait is nil, or xid is still running afterwards,
	// the waiter gets ErrWouldBlock.
	Wait func(xid types.TxnID)
}

// NewDB returns an empty database.
func NewDB() *DB { return &DB{Mgr: NewTxnManager(), Heap: &Heap{}} }

// Savepoint is one open savepoint: a name and the subtransaction xid it started.
type Savepoint struct {
	Name string
	Xid  types.TxnID
}

// Txn is the state of one backend's transaction.
type Txn struct {
	DB    *DB
	Xid   types.TxnID // top-level xid
	Level IsolationLevel
	// NextCid is the command id the next statement will use (starts at 0).
	NextCid CommandID
	// TxnSnap is the transaction snapshot under RepeatableRead (nil until the first statement).
	TxnSnap *Snapshot
	// Savepoints is the stack of open savepoints, innermost last.
	Savepoints []Savepoint
	// Subxids lists every subtransaction xid of this transaction that has not
	// been rolled back (including ones whose savepoint was released), ascending.
	Subxids []types.TxnID
}

// Begin starts a transaction at the given isolation level.
func (db *DB) Begin(level IsolationLevel) *Txn {
	return &Txn{DB: db, Xid: db.Mgr.Begin(), Level: level}
}

// Mine returns the xids that count as this transaction's own.
func (t *Txn) Mine() CurrentXids {
	return append(CurrentXids{t.Xid}, t.Subxids...)
}

// WriteXid is the xid stamped on new writes: the innermost open savepoint's
// subtransaction, or the top-level xid when no savepoint is open.
func (t *Txn) WriteXid() types.TxnID {
	if n := len(t.Savepoints); n > 0 {
		return t.Savepoints[n-1].Xid
	}
	return t.Xid
}

// Select runs "SELECT * FROM t" as one statement and returns the visible rows in heap order.
func (t *Txn) Select() []types.Row {
	snap := t.StatementSnapshot()
	return t.DB.Heap.Rows(t.DB.Heap.Scan(snap, t.DB.Mgr.Clog, t.Mine()))
}

// Insert runs "INSERT INTO t VALUES (row)" as one statement.
func (t *Txn) Insert(row types.Row) types.TID {
	snap := t.StatementSnapshot()
	return t.DB.Heap.Insert(t.WriteXid(), snap.CurCid, row)
}

// Commit commits the transaction (and its live subtransactions).
func (t *Txn) Commit() error { return t.DB.Mgr.Commit(t.Xid) }

// Abort rolls back the transaction (and its live subtransactions).
func (t *Txn) Abort() error { return t.DB.Mgr.Abort(t.Xid) }

// Savepoint runs "SAVEPOINT name": it starts a subtransaction whose parent is
// the current WriteXid. Names may repeat; the newest one wins.
func (t *Txn) Savepoint(name string) types.TxnID {
	parent := t.WriteXid()
	sub := t.DB.Mgr.Begin()
	t.DB.Mgr.Parent[sub] = parent
	t.Savepoints = append(t.Savepoints, Savepoint{Name: name, Xid: sub})
	t.Subxids = append(t.Subxids, sub)
	return sub
}

// ReleaseSavepoint runs "RELEASE SAVEPOINT name": the newest savepoint called
// name and every savepoint above it are closed. Their subtransactions stay
// live — their changes now simply belong to the enclosing (sub)transaction
// and commit or abort with it.
func (t *Txn) ReleaseSavepoint(name string) error {
	for i := len(t.Savepoints) - 1; i >= 0; i-- {
		if t.Savepoints[i].Name == name {
			t.Savepoints = t.Savepoints[:i]
			return nil
		}
	}
	return ErrNoSavepoint
}

// Problem 9.13 — StatementSnapshot [Medium]
//
// Start a new statement: assign its command id and return the snapshot it must use, according to the isolation level.
//
// READ COMMITTED takes a fresh snapshot for every statement, so each statement
// sees everything committed before it began (non-repeatable reads and phantoms
// are possible). REPEATABLE READ takes one snapshot at the transaction's FIRST
// statement (not at BEGIN!) and reuses it, so repeated reads return the same rows.
//
// Rules:
//  1. cid := t.NextCid; then t.NextCid++. (First statement uses cid 0.)
//  2. ReadCommitted: snap := t.DB.Mgr.GetSnapshot(t.Xid) (Problem 9.6) every time.
//  3. RepeatableRead: if t.TxnSnap is nil, set it to t.DB.Mgr.GetSnapshot(t.Xid).
//     The statement's snapshot is a COPY of *t.TxnSnap (t.TxnSnap itself must not change).
//  4. Set snap.CurCid = cid and return it.
//
// Example (T1 = 3 at RepeatableRead, T2 = 4 begins and commits before T1's first statement):
//
//	s := t1.StatementSnapshot()  // CurCid 0, Xmin 3, Xmax 5, Xip [] — T2 already finished
//	s = t1.StatementSnapshot()   // CurCid 1, same Xmin/Xmax/Xip even if new txns commit
//
// Constraints: one GetSnapshot per statement under RC, one per transaction under RR.
// Postgres reference: GetTransactionSnapshot() in src/backend/utils/time/snapmgr.c
func (t *Txn) StatementSnapshot() *Snapshot {
	panic("TODO: Problem 9.13")
}

// Problem 9.14 — UpdateWhere [Hard]
//
// Run "UPDATE t SET row = set(row) WHERE pred(row)" as one statement, handling concurrent updates per isolation level.
//
// Rules:
//  1. Take snap := t.StatementSnapshot() (Problem 9.13) once. Write with
//     xid = t.WriteXid(), cid = snap.CurCid, clog = t.DB.Mgr.Clog, mine = t.Mine().
//  2. Targets: the TIDs returned by Heap.Scan(snap, …) (Problem 9.12) whose
//     Data satisfies pred, in scan order. Versions created by this statement are
//     never targets (they have Cmin == CurCid, so they are invisible).
//  3. For each target tid, call Heap.Update(tid, xid, cid, set(v.Data), …)
//     (Problem 9.11) where v is the version at tid, and react to the result:
//     - TMOk: count it, next target.
//     - TMSelfModified or TMInvisible: skip it silently, next target.
//     - TMBeingModified: another running transaction holds the row. Call
//     t.DB.Wait(v.Xmax) if Wait is non-nil. If clog.Get(that xmax) is still
//     StatusInProgress afterwards, return (count so far, ErrWouldBlock).
//     Otherwise retry Update on the same tid.
//     - TMUpdated / TMDeleted under RepeatableRead: return (count so far,
//     ErrSerialization) — the snapshot is not allowed to see the new version.
//     - TMDeleted under ReadCommitted: the row is gone; skip it.
//     - TMUpdated under ReadCommitted ("EvalPlanQual"): move to the newer version
//     tid = v.Ctid. If pred(newer.Data) is false, skip the row; otherwise retry
//     Update on the newer tid — with set applied to the NEWER data.
//     Repeat for as many hops as needed.
//  4. Return (number of rows updated, nil).
//  5. On an error, rows already updated by this statement stay updated
//     (statement-level rollback is not modelled).
//
// Example (row (1, 100) committed; T1 and T2 both READ COMMITTED):
//
//	T2: UPDATE SET v = v*2 WHERE id=1  → 1   (row now (1,200), T2 running)
//	T1: UPDATE SET v = v+10 WHERE id=1 → T1 waits for T2 (Wait hook commits T2)
//	                                    → follows ctid, re-checks id=1 → (1,210), returns 1
//	Same under REPEATABLE READ for T1 → (0, ErrSerialization)
//
// Constraints: no goroutines; "waiting" happens only through t.DB.Wait.
// Postgres reference: ExecUpdate() + EvalPlanQual() in src/backend/executor/nodeModifyTable.c / execMain.c
func (t *Txn) Update(pred func(types.Row) bool, set func(types.Row) types.Row) (int, error) {
	panic("TODO: Problem 9.14")
}

// Problem 9.15 — RollbackTo [Hard]
//
// Run "ROLLBACK TO SAVEPOINT name": undo (by aborting xids) everything done since the savepoint, then re-open it.
//
// Each SAVEPOINT starts a subtransaction with its own xid (see the provided
// Savepoint method), and writes inside it are stamped with that xid. Rolling
// back to it therefore needs no undo log: aborting those xids in the clog makes
// their inserts invisible and their deletes ineffective — while the parent
// transaction keeps running.
//
// Rules:
//  1. Find the NEWEST savepoint called name (search t.Savepoints from the top).
//     None → ErrNoSavepoint, nothing changes.
//  2. Let sp be that savepoint. Every xid in t.Subxids that does not precede
//     sp.Xid (i.e. sp.Xid itself and every subtransaction started after it,
//     including those whose savepoints were released) is aborted:
//     set StatusAborted in the clog, delete it from Mgr.Running, and remove it
//     from t.Subxids.
//  3. Savepoints above sp are destroyed; sp itself stays on the stack
//     (t.Savepoints = t.Savepoints[:i+1]).
//  4. Re-open sp, like Postgres does: begin a fresh subtransaction xid with
//     Mgr.Begin() (Problem 9.4), record Mgr.Parent[new] = Mgr.Parent[old sp.Xid],
//     store it as the savepoint's Xid and append it to t.Subxids.
//
// Example (T = 3):
//
//	INSERT (1)                        -- xid 3
//	SAVEPOINT a                       -- sub xid 4
//	INSERT (2)                        -- xid 4
//	SAVEPOINT b                       -- sub xid 5
//	INSERT (3)                        -- xid 5
//	ROLLBACK TO a  → clog[4]=clog[5]=aborted, Savepoints=[a(6)], Subxids=[6]
//	SELECT         → (1)
//	INSERT (4)                        -- xid 6
//	COMMIT         → others see (1), (4)
//
// Constraints: O(|Subxids| + |Savepoints|).
// Postgres reference: RollbackToSavepoint() / AbortSubTransaction() in src/backend/access/transam/xact.c
func (t *Txn) RollbackTo(name string) error {
	panic("TODO: Problem 9.15")
}
