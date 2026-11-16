package txn

import (
	"slices"

	"github.com/natthphong/daTarbase/pkg/types"
)

// InfoMask holds a tuple's hint bits (Postgres: t_infomask HEAP_XMIN_* / HEAP_XMAX_*).
// A hint bit caches a clog lookup: once set, readers trust it and skip the clog.
type InfoMask uint16

const (
	XminCommitted InfoMask = 1 << 0 // HEAP_XMIN_COMMITTED: inserter is known committed
	XminInvalid   InfoMask = 1 << 1 // HEAP_XMIN_INVALID: inserter is known aborted
	XmaxCommitted InfoMask = 1 << 2 // HEAP_XMAX_COMMITTED: deleter is known committed
	XmaxInvalid   InfoMask = 1 << 3 // HEAP_XMAX_INVALID: deleter aborted / no deleter

	// XminFrozen (both xmin bits) means "inserted so long ago that it is visible
	// to everyone": xmin is no longer compared at all (HEAP_XMIN_FROZEN).
	XminFrozen = XminCommitted | XminInvalid
)

// Version is one tuple version in the heap (Postgres: HeapTupleHeaderData).
//
//	UPDATE never overwrites: it stamps Xmax on the old version and appends a
//	new one. The old version's Ctid points at the new one:
//
//	(0,1) {Xmin:3 Xmax:5 Ctid:(0,2) Data:(1,'a')}  ──►  (0,2) {Xmin:5 Xmax:0 Ctid:(0,2) Data:(1,'b')}
type Version struct {
	Xmin  types.TxnID // inserting transaction
	Xmax  types.TxnID // deleting / updating transaction, InvalidTxnID if none
	Cmin  CommandID   // command id of the insert (meaningful to the inserting txn only)
	Cmax  CommandID   // command id of the delete (meaningful to the deleting txn only)
	Ctid  types.TID   // own TID if this is the newest version, else the TID of the next version
	Hints InfoMask    // hint bits
	Data  types.Row   // column values
}

// TuplesPerPage is how many versions the in-memory heap puts on one "page"
// before moving to the next page number. TIDs look like real ctids: (page, slot)
// with slots numbered from 1 — (0,1) (0,2) (0,3) (0,4) (1,1) …
const TuplesPerPage = 4

// Heap is an append-only list of tuple versions addressed by TID.
// VACUUM removes a version by setting its entry to nil; TIDs never move.
type Heap struct {
	// Versions[i] lives at TIDOf(i). nil = removed by VACUUM (an unused line pointer).
	Versions []*Version
}

// TIDOf returns the TID of Versions[i].
func TIDOf(i int) types.TID {
	return types.TID{Page: types.PageID(i / TuplesPerPage), Slot: types.SlotID(i%TuplesPerPage + 1)}
}

// IndexOf returns the Versions index of tid, or -1 if tid is malformed (slot 0
// or slot > TuplesPerPage).
func IndexOf(tid types.TID) int {
	if tid.Slot < 1 || tid.Slot > TuplesPerPage {
		return -1
	}
	return int(tid.Page)*TuplesPerPage + int(tid.Slot) - 1
}

// Get returns the version at tid, or nil if tid is out of range or removed.
func (h *Heap) Get(tid types.TID) *Version {
	i := IndexOf(tid)
	if i < 0 || i >= len(h.Versions) {
		return nil
	}
	return h.Versions[i]
}

// Rows returns the Data of the versions at tids, in order.
func (h *Heap) Rows(tids []types.TID) []types.Row {
	out := make([]types.Row, 0, len(tids))
	for _, tid := range tids {
		out = append(out, h.Get(tid).Data)
	}
	return out
}

// CurrentXids is the set of xids that count as "my own transaction": the
// top-level xid plus every subtransaction xid that has NOT been rolled back
// (Postgres: TransactionIdIsCurrentTransactionId). nil = a transaction with no xid.
type CurrentXids []types.TxnID

// Contains reports whether xid is one of mine. InvalidTxnID is never mine.
func (c CurrentXids) Contains(xid types.TxnID) bool {
	return xid != types.InvalidTxnID && slices.Contains(c, xid)
}

// Problem 9.8 — TupleVisible [Hard]
//
// Decide whether tuple version v is visible to a statement using snapshot snap, setting hint bits along the way.
//
// This is the heart of MVCC. Every reader runs it on every version it meets.
// "Status of xid" means: use the hint bits if set, otherwise ask clog.Get.
//
// Rules — first decide whether the INSERT is visible:
//  1. (v.Hints & XminFrozen) == XminFrozen → the insert is visible; go to rule 8.
//  2. XminInvalid hint set → invisible.
//  3. XminCommitted hint set → if XidInSnapshot(v.Xmin, snap) invisible, else go to rule 8.
//  4. v.Xmin is mine (mine.Contains): my own insert.
//     a. v.Cmin >= snap.CurCid → invisible (inserted by this or a later statement).
//     b. v.Xmax == InvalidTxnID → visible.
//     c. v.Xmax is not mine → visible (the deleter can only be one of my
//     subtransactions that was rolled back).
//     d. otherwise visible iff v.Cmax >= snap.CurCid (deleted by a later statement).
//     Rule 4 never sets hint bits and returns directly.
//  5. XidInSnapshot(v.Xmin, snap) → invisible (inserter still running for us).
//  6. clog.Get(v.Xmin) == StatusCommitted → set XminCommitted, go to rule 8.
//  7. clog says StatusAborted → set XminInvalid, invisible. Any other status →
//     invisible, no hint.
//
// Rules — the insert is visible; now decide whether the DELETE hides it:
//  8. v.Xmax == InvalidTxnID or XmaxInvalid hint set → visible.
//  9. XmaxCommitted hint set → visible iff XidInSnapshot(v.Xmax, snap).
//  10. v.Xmax is mine → visible iff v.Cmax >= snap.CurCid.
//  11. XidInSnapshot(v.Xmax, snap) → visible (deleter still running for us).
//  12. clog.Get(v.Xmax) == StatusCommitted → set XmaxCommitted, invisible.
//     StatusAborted → set XmaxInvalid, visible. Any other status → visible, no hint.
//
// Example (clog: 3 committed, 4 aborted; snapshot {Xmin:5 Xmax:7 Xip:[5] CurCid:1}; mine = [6]):
//
//	{Xmin:3}                     → true,  Hints gains XminCommitted
//	{Xmin:4}                     → false, Hints gains XminInvalid
//	{Xmin:5}                     → false  (5 is running for this snapshot)
//	{Xmin:3 Xmax:4}              → true,  Hints gains XminCommitted|XmaxInvalid
//	{Xmin:6 Cmin:0}              → true   (my insert from an earlier statement)
//	{Xmin:6 Cmin:1}              → false  (my insert from this statement)
//	{Xmin:6 Cmin:0 Xmax:6 Cmax:0}→ false  (I inserted and then deleted it)
//
// Constraints: O(log |Xip|); only touch v.Hints.
// Postgres reference: HeapTupleSatisfiesMVCC() in src/backend/access/heap/heapam_visibility.c
func TupleVisible(v *Version, snap *Snapshot, clog *Clog, mine CurrentXids) bool {
	switch {
	case v.Hints&XminFrozen == XminFrozen:
	case v.Hints&XminInvalid != 0:
		return false
	case v.Hints&XminCommitted != 0:
		if XidInSnapshot(v.Xmin, snap) {
			return false
		}
	case mine.Contains(v.Xmin):
		if v.Cmin >= snap.CurCid {
			return false
		}
		if v.Xmax == types.InvalidTxnID || !mine.Contains(v.Xmax) {
			return true
		}
		return v.Cmax >= snap.CurCid
	case XidInSnapshot(v.Xmin, snap):
		return false
	default:
		switch clog.Get(v.Xmin) {
		case StatusCommitted:
			v.Hints |= XminCommitted
		case StatusAborted:
			v.Hints |= XminInvalid
			return false
		default:
			return false
		}
	}

	switch {
	case v.Xmax == types.InvalidTxnID || v.Hints&XmaxInvalid != 0:
		return true
	case v.Hints&XmaxCommitted != 0:
		return XidInSnapshot(v.Xmax, snap)
	case mine.Contains(v.Xmax):
		return v.Cmax >= snap.CurCid
	case XidInSnapshot(v.Xmax, snap):
		return true
	}
	switch clog.Get(v.Xmax) {
	case StatusCommitted:
		v.Hints |= XmaxCommitted
		return false
	case StatusAborted:
		v.Hints |= XmaxInvalid
	}
	return true
}

// Problem 9.9 — Heap.Insert [Easy]
//
// Append a new tuple version created by transaction xid in command cid and return its TID.
//
// Rules:
//  1. The new version goes at the end of h.Versions; its TID is TIDOf(index).
//  2. Header: Xmin = xid, Cmin = cid, Xmax = InvalidTxnID, Cmax = 0, Hints = 0,
//     Ctid = its own TID (a newest version points at itself), Data = row.
//
// Example:
//
//	h := &Heap{}
//	h.Insert(10, 0, R(1,"a"))  // (0,1)
//	h.Insert(10, 1, R(2,"b"))  // (0,2)
//	... 2 more ...             // (0,3), (0,4)
//	h.Insert(11, 0, R(5,"e"))  // (1,1)  — page 1 starts after TuplesPerPage=4 versions
//
// Constraints: amortised O(1).
// Postgres reference: heap_insert() in src/backend/access/heap/heapam.c
func (h *Heap) Insert(xid types.TxnID, cid CommandID, row types.Row) types.TID {
	tid := TIDOf(len(h.Versions))
	h.Versions = append(h.Versions, &Version{Xmin: xid, Cmin: cid, Ctid: tid, Data: row})
	return tid
}

// TMResult is the outcome of trying to delete / update / lock a tuple version
// (Postgres: TM_Result in src/include/access/tableam.h).
type TMResult uint8

const (
	TMOk            TMResult = iota // the version was modified
	TMInvisible                     // the version cannot be modified (missing, or its inserter is not committed / is this very command)
	TMSelfModified                  // already deleted / updated by my own transaction
	TMBeingModified                 // another running transaction already deleted / updated it — must wait
	TMUpdated                       // a committed transaction updated it (Ctid points to a newer version)
	TMDeleted                       // a committed transaction deleted it (Ctid points to itself)
)

func (r TMResult) String() string {
	return [...]string{"TM_Ok", "TM_Invisible", "TM_SelfModified", "TM_BeingModified", "TM_Updated", "TM_Deleted"}[r]
}

// xminStatus is the inserter's status, trusting hint bits first.
//
// solution-only
func xminStatus(v *Version, clog *Clog) TxnStatus {
	switch {
	case v.Hints&XminFrozen == XminFrozen, v.Hints&XminCommitted != 0:
		return StatusCommitted
	case v.Hints&XminInvalid != 0:
		return StatusAborted
	}
	return clog.Get(v.Xmin)
}

// xmaxStatus is the deleter's status, trusting hint bits first. A version with
// no Xmax reports StatusAborted ("nobody deleted it").
//
// solution-only
func xmaxStatus(v *Version, clog *Clog) TxnStatus {
	switch {
	case v.Xmax == types.InvalidTxnID, v.Hints&XmaxInvalid != 0:
		return StatusAborted
	case v.Hints&XmaxCommitted != 0:
		return StatusCommitted
	}
	return clog.Get(v.Xmax)
}

// Problem 9.10 — Heap.Delete [Medium]
//
// Mark the version at tid as deleted by transaction xid in command cid, or report why it can't be.
//
// MVCC never removes data on DELETE: it only stamps Xmax/Cmax. Before stamping,
// it must check whether someone else got there first — that is how write-write
// conflicts are detected. The caller has already decided the version is
// visible to its snapshot; Delete only looks at the header and the clog.
//
// "Status of xmin" = StatusCommitted if the XminCommitted (or frozen) hint is
// set, StatusAborted if only XminInvalid is set, otherwise clog.Get(Xmin).
// "Status of xmax" = StatusCommitted if the XmaxCommitted hint is set,
// StatusAborted if the XmaxInvalid hint is set, otherwise clog.Get(Xmax).
//
// Rules, checked in this order:
//  1. No version at tid (out of range or removed) → TMInvisible.
//  2. Xmin is mine: if v.Cmin >= cid → TMInvisible (inserted by this same command).
//     Xmin is not mine: status of xmin is not StatusCommitted → TMInvisible.
//  3. Xmax == InvalidTxnID → OK.
//  4. Xmax is mine → TMSelfModified.
//  5. Status of xmax: StatusAborted → OK (overwrite the failed delete).
//     StatusCommitted → TMDeleted if v.Ctid == tid, else TMUpdated.
//     Anything else (still running) → TMBeingModified.
//  6. OK means: set Xmax = xid, Cmax = cid, clear the XmaxCommitted and
//     XmaxInvalid hints, return TMOk. Ctid and Data are unchanged.
//     For every other result the version is left untouched.
//
// Example (clog: 3 committed, 4 aborted, 5 running; mine = [7]):
//
//	(0,1) {Xmin:3}                 Delete((0,1), 7, 0) → TMOk, Xmax=7
//	(0,2) {Xmin:3 Xmax:4}          Delete((0,2), 7, 0) → TMOk, Xmax=7 (4 aborted)
//	(0,3) {Xmin:3 Xmax:5}          Delete((0,3), 7, 0) → TMBeingModified
//	(0,4) {Xmin:3 Xmax:3 Ctid:(0,4)} → TMDeleted;  Ctid:(1,1) → TMUpdated
//
// Constraints: O(|mine|).
// Postgres reference: heap_delete() + HeapTupleSatisfiesUpdate() in heapam.c / heapam_visibility.c
func (h *Heap) Delete(tid types.TID, xid types.TxnID, cid CommandID, clog *Clog, mine CurrentXids) TMResult {
	v := h.Get(tid)
	if v == nil {
		return TMInvisible
	}
	if mine.Contains(v.Xmin) {
		if v.Cmin >= cid {
			return TMInvisible
		}
	} else if xminStatus(v, clog) != StatusCommitted {
		return TMInvisible
	}
	if v.Xmax != types.InvalidTxnID {
		if mine.Contains(v.Xmax) {
			return TMSelfModified
		}
		switch xmaxStatus(v, clog) {
		case StatusAborted:
		case StatusCommitted:
			if v.Ctid == tid {
				return TMDeleted
			}
			return TMUpdated
		default:
			return TMBeingModified
		}
	}
	v.Xmax, v.Cmax = xid, cid
	v.Hints &^= XmaxCommitted | XmaxInvalid
	return TMOk
}

// Problem 9.11 — Heap.Update [Medium]
//
// Replace the version at tid with a new version holding row: stamp the old one, append the new one, link them by ctid.
//
// Rules:
//  1. Perform exactly the checks of Delete (Problem 9.10). If the result is not
//     TMOk, return (types.TID{}, result) and leave the heap unchanged
//     (no new version is appended).
//  2. On success the old version has Xmax = xid, Cmax = cid (as in Delete) and
//     a new version is appended exactly as Insert (Problem 9.9) would:
//     Xmin = xid, Cmin = cid, Ctid = its own TID, Data = row.
//  3. The old version's Ctid is set to the new TID. Return (newTID, TMOk).
//
// Example (clog: 3 committed; mine = [7]):
//
//	(0,1) {Xmin:3 Ctid:(0,1) Data:(1,'a')}
//	h.Update((0,1), 7, 0, R(1,"b"), …) → ((0,2), TMOk)
//	(0,1) {Xmin:3 Xmax:7 Ctid:(0,2) Data:(1,'a')}   ← old data is NOT overwritten
//	(0,2) {Xmin:7 Ctid:(0,2) Data:(1,'b')}
//
// Constraints: O(|mine|) amortised.
// Postgres reference: heap_update() in src/backend/access/heap/heapam.c
func (h *Heap) Update(tid types.TID, xid types.TxnID, cid CommandID, row types.Row, clog *Clog, mine CurrentXids) (types.TID, TMResult) {
	if res := h.Delete(tid, xid, cid, clog, mine); res != TMOk {
		return types.TID{}, res
	}
	newTID := h.Insert(xid, cid, row)
	h.Get(tid).Ctid = newTID
	return newTID, TMOk
}

// Problem 9.12 — Heap.Scan [Easy]
//
// Sequential scan: return the TIDs of every version visible to snap, in heap order.
//
// Rules:
//  1. Walk h.Versions from index 0 upward, skipping removed (nil) entries.
//  2. Keep the TIDs for which TupleVisible (Problem 9.8) is true. TupleVisible
//     may set hint bits — that is expected.
//  3. Return an empty, non-nil slice when nothing is visible.
//
// Consequences worth noticing (the tests check them):
//   - ROLLBACK never undoes anything in the heap: an aborted transaction's
//     versions stay there, but its xid is aborted in the clog, so its inserts
//     vanish and its deletes / updates are ignored.
//   - COMMIT only flips a clog bit: snapshots taken afterwards see the change,
//     snapshots taken before it never do.
//
// Example: T3 inserts (1,'a') and commits; T4 updates it to (1,'b') and aborts:
//
//	heap: (0,1) {Xmin:3 Xmax:4 Ctid:(0,2) (1,'a')}, (0,2) {Xmin:4 (1,'b')}
//	Scan(fresh snapshot) → [(0,1)]   — (1,'a') is back, without undoing a byte
//
// Constraints: O(len(h.Versions) · log |Xip|).
// Postgres reference: heapgettup() / heapgetpage() in src/backend/access/heap/heapam.c
func (h *Heap) Scan(snap *Snapshot, clog *Clog, mine CurrentXids) []types.TID {
	out := []types.TID{}
	for i, v := range h.Versions {
		if v != nil && TupleVisible(v, snap, clog, mine) {
			out = append(out, TIDOf(i))
		}
	}
	return out
}
