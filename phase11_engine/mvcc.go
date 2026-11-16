package engine

import (
	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	txn "github.com/natthphong/daTarbase/phase09_txn"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 11.17 — TableHeap.Delete [Hard]
//
// MVCC delete of one tuple version: check who else touched it, then stamp xmax = my xid in place and WAL-log it.
//
// A DELETE never removes bytes. It writes the deleter's xid into the
// version's xmax; readers whose snapshot sees that xid as committed stop
// seeing the row. Before stamping, heap_delete asks "what state is this
// version in?" (HeapTupleSatisfiesUpdate) — another transaction may already
// have deleted it:
//
//	xmax                         result
//	0, or an aborted xid         Ok: stamp mine (an aborted deleter never happened)
//	my own xid                   nothing to do (deleted earlier in this transaction)
//	a running xid                ErrWriteConflict   (Postgres would wait for it to finish)
//	a committed xid              ErrSerialization   (a version I could see was deleted by someone I can't see)
//
// Rules (check in this order; on every error the page is unpinned unchanged):
//  1. tid.Page >= h.NumPages(), slot 0 (the LSN item), or no live tuple at tid
//     (GetTuple fails) → ErrTupleNotFound.
//  2. hdr := tuple.DecodeHeader(data). clog := t.db.mgr.Clog, mine := t.mine().
//  3. The insert must be valid for me: if hdr.Xmin is not mine and
//     clog.Get(hdr.Xmin) != StatusCommitted → ErrTupleNotFound (it was
//     inserted by an aborted or a still-running transaction).
//  4. hdr.Xmax != 0: mine → return nil without any change (no WAL);
//     clog says StatusInProgress → ErrWriteConflict; StatusCommitted →
//     ErrSerialization; StatusAborted → continue as if xmax were 0.
//     (Under READ COMMITTED Postgres would instead follow the update chain —
//     EvalPlanQual. Our statements run atomically, so a target an RC statement
//     just saw can never have a committed deleter.)
//  5. xid := t.assignXid(); setXmax(data, xid) in the pinned page;
//     lsn := h.wal.Append(xid, wal.RecUpdate, heapRecord{Op: opDelete, Rel:
//     h.Def.RelID, Page: tid.Page, Slot: tid.Slot, Xmax: xid}.encode());
//     setPageLSN(page, lsn); delete(h.vm, tid.Page); unpin dirty.
//  6. Index entries are NOT touched (VACUUM removes them, Problem 11.19).
//
// Example (emp: alice (0,1) committed):
//
//	t1.Delete(alice) → nil; header xmax = t1; t1 no longer sees alice, others still do
//	t1.Delete(alice) again → nil, no new WAL record
//	t2.Delete(alice) while t1 runs → ErrWriteConflict;  after t1 rolls back → nil (xmax = t2)
//	rr snapshot taken, t1 deletes bob and commits, rr.Delete(bob) → ErrSerialization
//	deleting a row inserted by an aborted transaction → ErrTupleNotFound
//
// Constraints: one page pinned; one WAL record per stamped version.
// Postgres reference: heap_delete() in src/backend/access/heap/heapam.c,
// HeapTupleSatisfiesUpdate() in heapam_visibility.c
func (h *TableHeap) Delete(t *Txn, tid types.TID) error {
	if tid.Page >= h.NumPages() || tid.Slot == lsnSlot {
		return ErrTupleNotFound
	}
	f, err := h.pool.FetchPage(tid.Page)
	if err != nil {
		return err
	}
	p := heapPage(f)
	data, err := p.GetTuple(tid.Slot)
	if err != nil {
		h.pool.UnpinPage(tid.Page, false)
		return ErrTupleNotFound
	}
	fail := func(err error) error { h.pool.UnpinPage(tid.Page, false); return err }
	hdr, err := tuple.DecodeHeader(data)
	if err != nil {
		return fail(err)
	}
	clog, mine := t.db.mgr.Clog, t.mine()
	if !mine.Contains(hdr.Xmin) && clog.Get(hdr.Xmin) != txn.StatusCommitted {
		return fail(ErrTupleNotFound)
	}
	if hdr.Xmax != types.InvalidTxnID {
		if mine.Contains(hdr.Xmax) {
			return fail(nil)
		}
		switch clog.Get(hdr.Xmax) {
		case txn.StatusInProgress, txn.StatusSubCommitted:
			return fail(ErrWriteConflict)
		case txn.StatusCommitted:
			return fail(ErrSerialization)
		}
	}
	xid := t.assignXid()
	setXmax(data, xid)
	lsn := h.wal.Append(xid, wal.RecUpdate, heapRecord{Op: opDelete, Rel: h.Def.RelID, Page: tid.Page, Slot: tid.Slot, Xmax: xid}.encode())
	setPageLSN(p, lsn)
	delete(h.vm, tid.Page)
	return h.pool.UnpinPage(tid.Page, true)
}
