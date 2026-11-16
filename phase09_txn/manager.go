package txn

import (
	"errors"

	"github.com/natthphong/daTarbase/pkg/types"
)

var (
	// ErrNotInProgress: the xid is not a running transaction (never begun, or
	// already committed / aborted).
	ErrNotInProgress = errors.New("txn: transaction is not in progress")
	// ErrSubxact: End was called with a subtransaction xid; only top-level
	// transactions commit or abort through End.
	ErrSubxact = errors.New("txn: cannot end a subtransaction directly")
)

// TxnManager hands out xids and records their fate (Postgres: varsup.c,
// procarray.c and xact.c rolled into one).
type TxnManager struct {
	// Clog stores the status of every xid.
	Clog *Clog
	// NextXid is the xid the next Begin will hand out (ShmemVariableCache->nextXid).
	NextXid types.TxnID
	// Running is the set of in-progress xids, top-level AND subtransactions (the proc array).
	Running map[types.TxnID]bool
	// Parent maps a subtransaction xid to the xid of the (sub)transaction that
	// created it (pg_subtrans). Top-level xids have no entry. Entries are never removed.
	Parent map[types.TxnID]types.TxnID
}

// NewTxnManager returns a manager whose first xid is FirstNormalTxnID (3).
func NewTxnManager() *TxnManager {
	return &TxnManager{
		Clog:    NewClog(),
		NextXid: FirstNormalTxnID,
		Running: map[types.TxnID]bool{},
		Parent:  map[types.TxnID]types.TxnID{},
	}
}

// TopLevel follows Parent links from xid up to its top-level transaction.
// A top-level xid (no Parent entry) is returned unchanged.
func (m *TxnManager) TopLevel(xid types.TxnID) types.TxnID {
	for {
		p, ok := m.Parent[xid]
		if !ok {
			return xid
		}
		xid = p
	}
}

// Commit commits top-level transaction xid (see Problem 9.5).
func (m *TxnManager) Commit(xid types.TxnID) error { return m.End(xid, true) }

// Abort aborts top-level transaction xid (see Problem 9.5).
func (m *TxnManager) Abort(xid types.TxnID) error { return m.End(xid, false) }

// Problem 9.4 — TxnManager.Begin [Easy]
//
// Start a transaction: hand out the next xid, mark it running and in progress.
//
// Rules:
//  1. Return m.NextXid and advance m.NextXid by one.
//  2. Wraparound: xids 0, 1, 2 are special and never handed out. If advancing
//     makes NextXid wrap past 4294967295, NextXid becomes FirstNormalTxnID (3).
//  3. Record the new xid in m.Running.
//  4. Write StatusInProgress for the new xid into m.Clog. After a wraparound the
//     clog may still hold an old status for that xid from 2^32 transactions ago
//     — it must be overwritten.
//
// Example:
//
//	m := NewTxnManager()
//	m.Begin()                 // 3, NextXid = 4
//	m.Begin()                 // 4, NextXid = 5
//	m.NextXid = 4294967295
//	m.Begin()                 // 4294967295, NextXid = 3   (wrapped, skipping 0,1,2)
//
// Constraints: O(1).
// Postgres reference: GetNewTransactionId() in src/backend/access/transam/varsup.c
func (m *TxnManager) Begin() types.TxnID {
	panic("TODO: Problem 9.4")
}

// Problem 9.5 — TxnManager.End [Easy]
//
// Commit (commit=true) or abort (commit=false) a top-level transaction and all its running subtransactions.
//
// A transaction's result becomes visible to the world in one step: the clog bit
// flips from in-progress to committed (or aborted) and the xid leaves the proc array.
// Subtransactions (savepoints, Problem 9.15) that are still running are part of
// the same atomic outcome.
//
// Rules:
//  1. xid not in m.Running → ErrNotInProgress (this covers "commit twice",
//     "commit after abort" and "never begun"). Nothing changes.
//  2. xid has a Parent entry (it is a subtransaction) → ErrSubxact. Nothing changes.
//  3. The affected set is xid plus every xid in m.Running whose Parent chain
//     leads to xid (children, grandchildren, …).
//  4. Set every affected xid to StatusCommitted (commit) or StatusAborted (abort)
//     in the clog and delete it from m.Running. Subtransactions that are already
//     aborted (no longer running) are not touched.
//
// Example:
//
//	m := NewTxnManager()
//	a, b := m.Begin(), m.Begin()   // 3, 4
//	m.End(a, true)                 // nil; clog[3]=committed, Running={4}
//	m.End(a, false)                // ErrNotInProgress
//	m.End(b, false)                // nil; clog[4]=aborted
//
// Constraints: O(running xids × nesting depth).
// Postgres reference: RecordTransactionCommit() / RecordTransactionAbort() in xact.c,
// TransactionIdCommitTree() in transam.c
func (m *TxnManager) End(xid types.TxnID, commit bool) error {
	panic("TODO: Problem 9.5")
}

// CommandID numbers the statements inside one transaction (Postgres: CommandId,
// cmin/cmax). A transaction's own changes made by command c are visible to its
// commands > c, but not to command c itself.
type CommandID uint32

// Snapshot is an MVCC snapshot (Postgres: SnapshotData).
//
//	      finished          some running          not yet started
//	──────────────────┬───────────────────────┬────────────────────►  xids
//	                 Xmin                    Xmax
//
// An xid is "in progress as far as this snapshot is concerned" if it is >= Xmax,
// or it is listed in Xip. Everything that precedes Xmin was finished when the
// snapshot was taken.
type Snapshot struct {
	Xmin types.TxnID   // oldest xid that was still running (or Xmax if none)
	Xmax types.TxnID   // first xid not yet assigned when the snapshot was taken
	Xip  []types.TxnID // running xids (other transactions only), ascending by TxnIDPrecedes
	// CurCid is the command id of the statement using this snapshot. Changes made
	// by my own transaction with cid >= CurCid are not visible.
	CurCid CommandID
}

// Problem 9.6 — GetSnapshot [Medium]
//
// Take an MVCC snapshot of which transactions are running, as seen by transaction myXid.
//
// Rules:
//  1. Xmax = m.NextXid.
//  2. Xip = every xid in m.Running EXCEPT my own: myXid itself and every running
//     xid whose Parent chain leads to myXid (my subtransactions). Sort Xip
//     ascending using TxnIDPrecedes (Problem 9.3). When nothing qualifies, nil
//     and an empty slice are both accepted.
//  3. Xmin = the oldest xid in m.Running (by TxnIDPrecedes), INCLUDING my own
//     xids. If nothing is running, Xmin = Xmax.
//  4. myXid may be InvalidTxnID (a read-only snapshot): then nothing is excluded.
//  5. CurCid = 0 (the caller sets it).
//
// Example:
//
//	m := NewTxnManager()
//	t3, t4, t5 := m.Begin(), m.Begin(), m.Begin()   // 3, 4, 5
//	m.Commit(t4)
//	m.GetSnapshot(t5)  // {Xmin: 3, Xmax: 6, Xip: [3]}
//	m.GetSnapshot(0)   // {Xmin: 3, Xmax: 6, Xip: [3 5]}
//
// Constraints: O(n log n) in the number of running xids.
// Postgres reference: GetSnapshotData() in src/backend/storage/ipc/procarray.c
func (m *TxnManager) GetSnapshot(myXid types.TxnID) *Snapshot {
	panic("TODO: Problem 9.6")
}

// Problem 9.7 — XidInSnapshot [Medium]
//
// Report whether xid must be treated as still running by someone using snapshot snap.
//
// Rules:
//  1. Special xids (0, 1, 2) → false.
//  2. xid precedes snap.Xmin (TxnIDPrecedes) → false: it finished before the snapshot.
//  3. xid does not precede snap.Xmax (xid >= Xmax, circularly) → true: it started
//     after the snapshot was taken.
//  4. Otherwise → true iff xid is listed in snap.Xip.
//
// Example (snapshot {Xmin: 100, Xmax: 105, Xip: [100 102]}):
//
//	99 → false   100 → true   101 → false   102 → true
//	104 → false  105 → true   200 → true    2 → false
//
// Constraints: O(log |Xip|) — Xip is sorted, use binary search.
// Postgres reference: XidInMVCCSnapshot() in src/backend/utils/time/snapmgr.c
func XidInSnapshot(xid types.TxnID, snap *Snapshot) bool {
	panic("TODO: Problem 9.7")
}
