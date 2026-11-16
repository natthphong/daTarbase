package wal

import (
	"maps"
	"sync"

	"github.com/natthphong/daTarbase/pkg/types"
)

// DB glues a Disk, a Log and a BufferPool into a tiny transactional store:
// transactions overwrite byte ranges of pages, every change is logged first,
// and Recover (Problem 10.14) brings the store back after a crash.
//
// DB methods are NOT safe for concurrent use, with one exception: Commit may be
// called from many goroutines at once (each for a different transaction).
type DB struct {
	Disk *Disk
	Log  *Log
	BP   *BufferPool

	mu      sync.Mutex                // guards att and nextTxn
	att     map[types.TxnID]types.LSN // Active Transaction Table: txn → LastLSN (its most recent record)
	nextTxn types.TxnID               // id the next Begin hands out
}

// Open starts a DB over d. In-memory state starts empty: Open does NOT recover
// (call Recover for that). On a fresh disk you can start working right away.
func Open(d *Disk) *DB {
	l := newLog(d)
	return &DB{Disk: d, Log: l, BP: NewBufferPool(d, l), att: map[types.TxnID]types.LSN{}, nextTxn: 1}
}

// Begin starts a transaction: it hands out the next txn id, appends a BEGIN
// record (PrevLSN = InvalidLSN) and enters the txn into the ATT.
func (db *DB) Begin() types.TxnID {
	db.mu.Lock()
	defer db.mu.Unlock()
	txn := db.nextTxn
	db.nextTxn++
	db.att[txn] = db.Log.Append(Record{Txn: txn, Type: RecBegin})
	return txn
}

// Active returns a copy of the Active Transaction Table.
func (db *DB) Active() map[types.TxnID]types.LSN {
	db.mu.Lock()
	defer db.mu.Unlock()
	return maps.Clone(db.att)
}

// Abort rolls a running transaction back (Problem 10.13 does the work).
func (db *DB) Abort(txn types.TxnID) error { return db.Undo(txn) }

// Crash simulates a power failure and returns what survives: a copy of the disk.
// leak > 0 additionally lets the first `leak` bytes of the unflushed WAL tail
// reach the disk (an OS write that happened without the fsync completing),
// which usually leaves a torn record at the end of the log.
func (db *DB) Crash(leak int) *Disk {
	d := db.Disk.Clone()
	db.Log.mu.Lock()
	tail := db.Log.buf[db.Log.flushed:]
	d.AppendGarbageWAL(tail[:min(leak, len(tail))])
	db.Log.mu.Unlock()
	return d
}

// Problem 10.7 — Update [Medium]
//
// Overwrite bytes [off, off+len(data)) of a page on behalf of txn: log first (a full-page image if the page was clean, then an UPDATE record), then change the page.
//
// Physical logging: the UPDATE record stores the page, the offset, and the
// bytes before and after, so redo can re-apply `after` and undo can put back
// `before`.
//
// Full-page writes: a page write that is cut short by a crash leaves a TORN page
// (half old bytes, half new). Redo cannot fix a torn page from small deltas,
// because the deltas assume the rest of the page is intact. So whenever a CLEAN
// page is about to be dirtied, we first log its complete image (RecFPI). Redo
// restores that image and replays the deltas on top of it. Because the FPI is
// the first record of the page's dirty period, it is exactly the page's recLSN.
//
// Rules:
//  1. txn not in the ATT → 0, ErrNoTxn. len(data) == 0 or off+len(data) >
//     PageDataSize → 0, ErrBadRange. On error nothing is logged or changed.
//  2. p := db.BP.Page(page). If the page is clean (not in db.BP.dirty), append
//     Record{Type: RecFPI, Txn: InvalidTxnID, PrevLSN: InvalidLSN,
//     Body: FPIBody{page, p.Data}.Encode()} — the image BEFORE this change —
//     and set db.BP.dirty[page] = that FPI's LSN. Then set p.LSN to the FPI's
//     LSN (the page now matches the image).
//  3. Append Record{Type: RecUpdate, Txn: txn, PrevLSN: ATT[txn],
//     Body: UpdateBody{page, off, before, data}.Encode()} where before is a copy
//     of p.Data[off:off+len(data)].
//  4. Apply: copy data into p.Data[off:], set p.LSN = update LSN and
//     ATT[txn] = update LSN. Return the update LSN.
//  5. Nothing is flushed.
//
// Example (fresh DB):
//
//	t := db.Begin()                    // BEGIN@8, End=37
//	db.Update(t, 3, 4, []byte("hi"))   // FPI@37 (97 B), UPDATE@134 (41 B) → 134; End=175
//	                                   // page 3: LSN 134, Data[4:6]="hi", recLSN 37
//	db.Update(t, 3, 5, []byte("ey"))   // page dirty → no FPI; UPDATE@175 → 175; End=216
//	                                   // its before-image is "i\x00"; Data[4:7]="hey"
//	db.Update(t, 3, 63, []byte("xy"))  // → ErrBadRange (65 > 64)
//
// Constraints: O(len(data)) besides the 64-byte image.
// Postgres reference: XLogInsert() + XLogRecordAssemble()'s doPageWrites / XLogCheckBufferNeedsBackup() in src/backend/access/transam/xloginsert.c
func (db *DB) Update(txn types.TxnID, page types.PageID, off int, data []byte) (types.LSN, error) {
	panic("TODO: Problem 10.7")
}

// Problem 10.8 — Commit [Medium]
//
// Commit txn: append a COMMIT record, remove txn from the ATT, and return only once the commit record is durable.
//
// "Committed" means "the COMMIT record is on disk" — not "the data pages are on
// disk". The pages can be written whenever the buffer pool likes; redo will
// rebuild them from the log. That is why a commit costs one sequential WAL
// fsync instead of random writes of every touched page (Postgres: no-force).
//
// Rules:
//  1. txn not in the ATT → 0, ErrNoTxn (nothing appended).
//  2. Under db.mu: append Record{Type: RecCommit, Txn: txn, PrevLSN: ATT[txn]}
//     and delete txn from the ATT.
//  3. Release db.mu, THEN call db.Log.FlushUpTo(commitLSN) (Problem 10.5).
//     Holding db.mu during the flush would serialise all committers behind one
//     fsync each and defeat group commit.
//  4. Return the commit record's LSN.
//
// Example (continuing 10.7's example):
//
//	db.Commit(t)   // COMMIT@216 → 216; FlushedLSN()=245, Fsyncs()=1
//	db.Commit(t)   // → ErrNoTxn
//
//	// 10 transactions committing concurrently while the first fsync is stuck in
//	// the device → 2 fsyncs in total.
//
// Constraints: commits of different transactions may run concurrently; -race clean.
// Postgres reference: RecordTransactionCommit() in src/backend/access/transam/xact.c
func (db *DB) Commit(txn types.TxnID) (types.LSN, error) {
	panic("TODO: Problem 10.8")
}

// Problem 10.10 — Checkpoint [Easy]
//
// Take a fuzzy checkpoint: log a snapshot of the ATT and DPT, make it durable, then point the master record at it.
//
// Without checkpoints recovery would have to scan the WAL from the very
// beginning. A checkpoint records "what was in flight" so the analysis pass
// (Problem 10.11) can start there. It is FUZZY: it does not write any data
// pages and does not stop transactions — the DPT inside it tells redo how far
// back it must go for pages that were still dirty.
//
// Rules:
//  1. Build CheckpointBody{NextTxn: db.nextTxn, ATT: one entry per ATT txn,
//     DPT: one entry per db.BP.dirty page (with its recLSN)}. (Encode sorts them.)
//  2. Append Record{Type: RecCheckpoint, Txn: 0, PrevLSN: 0, Body: body.Encode()}.
//  3. db.Log.FlushUpTo(checkpoint LSN).
//  4. Only after that, db.Disk.SetMaster(checkpoint LSN). (If the master pointed
//     at a record that is not durable, recovery could not find it.)
//  5. Write no data pages. Return the checkpoint LSN.
//
// Example:
//
//	// ATT {2:301}, dirty {1:37, 2:204}, nextTxn 3, End=371
//	db.Checkpoint() // → 371; record body 48 bytes (4+4+12+4+24), End=448,
//	                // FlushedLSN()=448, Disk.Master()=371, no page written
//
// Constraints: O(|ATT| + |DPT|).
// Postgres reference: CreateCheckPoint() in src/backend/access/transam/xlog.c (Postgres also flushes all dirty buffers — see README)
func (db *DB) Checkpoint() (types.LSN, error) {
	panic("TODO: Problem 10.10")
}
