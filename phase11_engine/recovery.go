package engine

import (
	wal "github.com/natthphong/daTarbase/phase10_wal"
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
	panic("TODO: Problem 11.20")
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
	panic("TODO: Problem 11.21")
}
