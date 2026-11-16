package engine

import (
	"errors"
	"fmt"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	btree "github.com/natthphong/daTarbase/phase05_btree"
	txn "github.com/natthphong/daTarbase/phase09_txn"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/types"
)

// VacuumStats reports what one VACUUM did.
type VacuumStats struct {
	Scanned     int // tuple versions examined
	Removed     int // dead versions removed from the heap (and from every index)
	PagesPruned int // pages that had at least one dead version
	AllVisible  int // pages marked all-visible in the visibility map afterwards
}

// Problem 11.19 — DB.Vacuum [Hard]
//
// VACUUM one table: remove versions no snapshot can ever see, together with their index entries, compact the pages, and refresh the FSM and visibility map.
//
// Every UPDATE, DELETE and aborted INSERT leaves a dead version behind. Its
// bytes stay until VACUUM proves nobody can see it any more:
//
//	horizon = db.oldestXmin()      oldest xid any running transaction / RR snapshot may consider running
//	dead    = xmin aborted
//	       or xmin committed, xmax committed and xmax precedes the horizon
//
// Rules:
//  1. db.closed → ErrClosed; unknown table → wraps ErrNoSuchTable.
//     horizon := db.oldestXmin(), clog := db.mgr.Clog.
//  2. For every page 0..NumPages()-1 (pin, heapPage) and every live tuple
//     except the LSN item: Scanned++, decode the header and classify it with
//     clog.Get (rules above; "precedes" = txn.TxnIDPrecedes). A version whose
//     xmin is in progress, or whose deleter is in progress / aborted / not old
//     enough ("recently dead"), is kept.
//  3. For each dead version FIRST remove its index entries: deform the tuple,
//     and for every index of the table: item := ix.Entry(row, tid);
//     ix.Tree.Delete(item.Key, tid). btree.ErrNotFound is ignored, other errors
//     are returned. (Index entries must go before the slot can be reused by a
//     new tuple.)
//  4. If the page had dead versions: DeleteTuple each dead slot, then Compact()
//     the page; log it — lsn := db.wal.Append(0, wal.RecUpdate,
//     heapRecord{Op: opPrune, Rel, Page, Slots: the dead slots in ascending order}.encode())
//     — setPageLSN, PagesPruned++, Removed += count; unpin dirty. Otherwise
//     unpin clean.
//  5. Visibility map: if every version left on the page has xmin committed and
//     preceding the horizon, and xmax 0 or aborted, set h.vm[page] = true and
//     AllVisible++; otherwise delete(h.vm, page). (An empty page is all-visible.)
//  6. h.fsm.Update(page, FreeSpace()) for every page.
//
// Example (50 employees on page 0 (slots 1..50), committed by xid 3; xid 4 deletes
// ids 1..10 and commits; xid 5 inserts a 51st row and rolls back; nothing running):
//
//	Vacuum("emp") → {Scanned:51 Removed:11 PagesPruned:1 AllVisible:1}
//	                WAL: prune of page 0, slots [1..10 51]; each index keeps 40 entries;
//	                the next insert reuses slot (0,1)
//	same, but an RR transaction took its snapshot before xid 4 committed → the
//	10 deleted rows are "recently dead": Removed 1, AllVisible 0
//
// Constraints: one pass over the heap; index entries removed before heap slots.
// Postgres reference: heap_vacuum_rel() / lazy_scan_prune() in
// src/backend/access/heap/vacuumlazy.c, HeapTupleSatisfiesVacuum()
func (db *DB) Vacuum(table string) (VacuumStats, error) {
	var st VacuumStats
	if db.closed {
		return st, ErrClosed
	}
	h := db.heaps[table]
	if h == nil {
		return st, fmt.Errorf("%w: %q", ErrNoSuchTable, table)
	}
	horizon, clog := db.oldestXmin(), db.mgr.Clog
	indexes := db.indexesOf(table)
	for pid := types.PageID(0); pid < h.NumPages(); pid++ {
		f, err := h.pool.FetchPage(pid)
		if err != nil {
			return st, err
		}
		p := heapPage(f)
		var dead []types.SlotID
		allVisible := true
		var ferr error
		for slot, data := range p.Tuples() {
			if slot == lsnSlot {
				continue
			}
			st.Scanned++
			hdr, err := tuple.DecodeHeader(data)
			if err != nil {
				ferr = err
				break
			}
			xmin, xmax := clog.Get(hdr.Xmin), txn.StatusAborted
			if hdr.Xmax != types.InvalidTxnID {
				xmax = clog.Get(hdr.Xmax)
			}
			isDead := xmin == txn.StatusAborted ||
				(xmin == txn.StatusCommitted && hdr.Xmax != types.InvalidTxnID &&
					xmax == txn.StatusCommitted && txn.TxnIDPrecedes(hdr.Xmax, horizon))
			if !isDead {
				if xmin != txn.StatusCommitted || !txn.TxnIDPrecedes(hdr.Xmin, horizon) || xmax != txn.StatusAborted {
					allVisible = false
				}
				continue
			}
			row, err := tuple.DeformTuple(h.Def.Schema, data)
			if err != nil {
				ferr = err
				break
			}
			tid := types.TID{Page: pid, Slot: slot}
			for _, ix := range indexes {
				item, err := ix.Entry(row, tid)
				if err == nil {
					err = ix.Tree.Delete(item.Key, tid)
				}
				if err != nil && !errors.Is(err, btree.ErrNotFound) {
					ferr = err
					break
				}
			}
			if ferr != nil {
				break
			}
			dead = append(dead, slot)
		}
		if ferr != nil {
			h.pool.UnpinPage(pid, false)
			return st, ferr
		}
		if len(dead) > 0 {
			for _, s := range dead {
				if err := p.DeleteTuple(s); err != nil {
					h.pool.UnpinPage(pid, true)
					return st, err
				}
			}
			p.Compact()
			lsn := db.wal.Append(types.InvalidTxnID, wal.RecUpdate, heapRecord{Op: opPrune, Rel: h.Def.RelID, Page: pid, Slots: dead}.encode())
			setPageLSN(p, lsn)
			st.PagesPruned++
			st.Removed += len(dead)
		}
		if allVisible {
			h.vm[pid] = true
			st.AllVisible++
		} else {
			delete(h.vm, pid)
		}
		h.fsm.Update(pid, p.FreeSpace())
		if err := h.pool.UnpinPage(pid, len(dead) > 0); err != nil {
			return st, err
		}
	}
	return st, nil
}
