package engine

import (
	"fmt"
	"path/filepath"

	txn "github.com/natthphong/daTarbase/phase09_txn"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/types"
)

// RecoveryStats reports what crash recovery did.
type RecoveryStats struct {
	Records   int // records after the last checkpoint
	Applied   int // heap records redone on their page
	Skipped   int // heap records whose page already had them (page LSN >= record LSN)
	Committed int // COMMIT records seen
	Aborted   int // ABORT records seen
}

// Problem 11.20 — DB.Checkpoint [Medium]
//
// Make everything up to now durable in the data files, so crash recovery can start after this point.
//
// After a checkpoint, recovery only replays the WAL records written AFTER the
// checkpoint record (the "tail", see OpenWith). That is only correct if every
// change logged before it is already in the data files and in pg_xact — so
// the checkpoint record must be written last:
//
//	flush WAL ─► flush every dirty page + fsync files ─► save pg_xact ─► CHECKPOINT record, flushed
//
// Rules:
//  1. db.closed → ErrClosed.
//  2. db.wal.Flush(db.wal.End()) — the whole WAL is durable.
//  3. For every table in catalog order: h.pool.FlushAll() (each page write
//     goes through walDisk, which honours the WAL rule), then h.disk.fsync().
//  4. saveClog(filepath.Join(db.dir, "pg_xact"), db.mgr.Clog, !db.opts.NoSync).
//     Recovery will not see commit records from before the checkpoint, so
//     their outcome must be in pg_xact.
//  5. lsn := db.wal.Append(0, wal.RecCheckpoint, encodeCheckpoint(db.mgr.NextXid)),
//     then db.wal.Flush(lsn). Every error is returned immediately — a failed
//     checkpoint must not write its record.
//
// Example:
//
//	tx inserts 3 rows and commits; db.Checkpoint()
//	→ no dirty page left; pg_xact says committed; last WAL record CHECKPOINT (body NextXid 4)
//	db.Crash(); Open(dir) → Recovery == RecoveryStats{} (empty tail), the 3 rows are there
//	a transaction still running at the checkpoint whose pages were flushed:
//	→ after Crash + Open its rows are invisible (OpenWith marks it aborted)
//
// Constraints: the checkpoint record is the last thing written.
// Postgres reference: CreateCheckPoint() in src/backend/access/transam/xlog.c
func (db *DB) Checkpoint() error {
	if db.closed {
		return ErrClosed
	}
	if err := db.wal.Flush(db.wal.End()); err != nil {
		return err
	}
	for _, td := range db.cat.Tables {
		h := db.heaps[td.Name]
		if err := h.pool.FlushAll(); err != nil {
			return err
		}
		if err := h.disk.fsync(); err != nil {
			return err
		}
	}
	if err := saveClog(filepath.Join(db.dir, "pg_xact"), db.mgr.Clog, !db.opts.NoSync); err != nil {
		return err
	}
	lsn := db.wal.Append(types.InvalidTxnID, wal.RecCheckpoint, encodeCheckpoint(db.mgr.NextXid))
	return db.wal.Flush(lsn)
}

// Problem 11.21 — DB.Recover [Hard]
//
// Crash recovery: replay the WAL tail onto the data files (redo), and rebuild the commit status of the transactions it mentions.
//
// After a crash the heap files hold some unpredictable mix of old and new
// pages: a page reaches disk only when it is evicted or checkpointed. The
// WAL, however, is complete up to the last flush. Recovery repeats history:
// every heap record is applied to its page unless the page already has it,
// which the page LSN tells (Postgres' XLogReadBufferForRedo: BLK_NEEDS_REDO
// vs BLK_DONE). Like Postgres — and unlike full ARIES — there is no undo:
// changes of transactions that never committed stay on the pages, invisible
// because their xid will be marked aborted.
//
// Rules — for each record of tail, in order (stats.Records = len(tail)):
//  1. RecUpdate: r := decodeHeapRecord(rec.Body) (errors as-is). The table is
//     the one whose TableDef.RelID == r.Rel (none → wraps ErrCorruptWAL).
//     While h.NumPages() <= r.Page, h.disk.AllocatePage() (the file may have
//     been shorter than the log assumes). Pin r.Page and take heapPage.
//     - pageLSN(page) >= rec.LSN → already applied: stats.Skipped++, unpin clean.
//     - otherwise apply it, setPageLSN(page, rec.LSN), unpin dirty, stats.Applied++:
//     opInsert: page.InsertTuple(r.Tuple) must return r.Slot, else
//     ErrRedoMismatch (the page is in exactly the state it had before the
//     original insert, and InsertTuple is deterministic).
//     opDelete: overwrite the xmax of the tuple in r.Slot with r.Xmax (setXmax).
//     opPrune: DeleteTuple every slot of r.Slots, then Compact().
//  2. RecCommit: clog → StatusCommitted for rec.Txn; stats.Committed++.
//     RecAbort: clog → StatusAborted; stats.Aborted++.
//  3. Any record with rec.Txn != 0 and !TxnIDPrecedes(rec.Txn, mgr.NextXid):
//     mgr.NextXid = rec.Txn + 1 (never hand out an xid the log already used).
//  4. Other record types are only counted in Records. Errors stop recovery
//     and are returned (with the pages left unpinned).
//
// (OpenWith then marks every still-in-progress xid aborted, rebuilds FSMs and
// indexes, and takes a checkpoint.)
//
// Example:
//
//	emp: 3 inserts + COMMIT flushed, no page written; Crash; Open
//	  → Recovery {Records:4 Applied:3 Skipped:0 Committed:1 Aborted:0}; Crash; Open → RecoveryStats{}
//	pool of 4 frames: 5000 rows committed (xid 3); xid 4 inserts 300 rows and deletes 100
//	without committing; xid 5 inserts 1 row and commits (flushing xid 4's records); Crash; Open
//	  → {Records:5403 Applied:101 Skipped:5300 Committed:2}: most pages had been evicted
//	    (stolen) before the crash; xid 4 → aborted; its rows invisible; next xid 6
//
// Constraints: one pass over the tail; idempotent (running it twice gives the same pages).
// Postgres reference: PerformWalRecovery() in src/backend/access/transam/xlogrecovery.c,
// heap_xlog_insert() / heap_xlog_delete() in src/backend/access/heap/heapam.c
func (db *DB) Recover(tail []wal.Record) (RecoveryStats, error) {
	st := RecoveryStats{Records: len(tail)}
	byRel := map[uint32]*TableHeap{}
	for _, h := range db.heaps {
		byRel[h.Def.RelID] = h
	}
	clog := db.mgr.Clog
	for _, rec := range tail {
		if rec.Txn != types.InvalidTxnID && !txn.TxnIDPrecedes(rec.Txn, db.mgr.NextXid) {
			db.mgr.NextXid = rec.Txn + 1
		}
		switch rec.Type {
		case wal.RecCommit:
			if err := clog.Set(rec.Txn, txn.StatusCommitted); err != nil {
				return st, err
			}
			st.Committed++
		case wal.RecAbort:
			if err := clog.Set(rec.Txn, txn.StatusAborted); err != nil {
				return st, err
			}
			st.Aborted++
		case wal.RecUpdate:
			r, err := decodeHeapRecord(rec.Body)
			if err != nil {
				return st, err
			}
			h := byRel[r.Rel]
			if h == nil {
				return st, fmt.Errorf("%w: unknown relation %d at LSN %d", ErrCorruptWAL, r.Rel, rec.LSN)
			}
			for h.NumPages() <= r.Page {
				if _, err := h.disk.AllocatePage(); err != nil {
					return st, err
				}
			}
			applied, err := redoHeap(h, r, rec.LSN)
			if err != nil {
				return st, err
			}
			if applied {
				st.Applied++
			} else {
				st.Skipped++
			}
		}
	}
	return st, nil
}

// redoHeap applies one heap record to its page unless the page already has it.
//
// solution-only
func redoHeap(h *TableHeap, r heapRecord, lsn types.LSN) (bool, error) {
	f, err := h.pool.FetchPage(r.Page)
	if err != nil {
		return false, err
	}
	p := heapPage(f)
	if pageLSN(p) >= lsn {
		return false, h.pool.UnpinPage(r.Page, false)
	}
	switch r.Op {
	case opInsert:
		slot, err := p.InsertTuple(r.Tuple)
		if err == nil && slot != r.Slot {
			err = fmt.Errorf("%w: insert at LSN %d went to slot %d, log says %d", ErrRedoMismatch, lsn, slot, r.Slot)
		}
		if err != nil {
			h.pool.UnpinPage(r.Page, false)
			return false, err
		}
	case opDelete:
		data, err := p.GetTuple(r.Slot)
		if err != nil {
			h.pool.UnpinPage(r.Page, false)
			return false, fmt.Errorf("%w: delete at LSN %d: %v", ErrRedoMismatch, lsn, err)
		}
		setXmax(data, r.Xmax)
	case opPrune:
		for _, s := range r.Slots {
			if err := p.DeleteTuple(s); err != nil {
				h.pool.UnpinPage(r.Page, true)
				return false, fmt.Errorf("%w: prune at LSN %d: %v", ErrRedoMismatch, lsn, err)
			}
		}
		p.Compact()
	}
	setPageLSN(p, lsn)
	return true, h.pool.UnpinPage(r.Page, true)
}
