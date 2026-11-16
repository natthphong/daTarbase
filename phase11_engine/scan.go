package engine

import (
	txn "github.com/natthphong/daTarbase/phase09_txn"

	"github.com/natthphong/daTarbase/pkg/types"
)

// SeqScan reads a whole table and returns the rows visible to a snapshot
// (Postgres: SeqScan over heapgettup_pagemode). It is an exec.Iterator.
type SeqScan struct {
	Heap    *TableHeap
	Snap    *txn.Snapshot   // the statement's snapshot
	Clog    *txn.Clog       // commit status of xids
	Mine    txn.CurrentXids // the scanning transaction's own xids
	EmitTID bool            // append the version's TID as an extra int8 "ctid" column (see tidValue)

	open    bool
	nblocks types.PageID // relation size when the scan was opened
	next    types.PageID // next page to read
	buf     []types.Row  // visible rows of the page read last
	pos     int          // next index of buf to return

	PagesRead int // pages fetched from the buffer pool since Open
}

// Open starts the scan at page 0; pages added after Open are not scanned
// (Postgres also fixes rs_nblocks when the scan starts).
func (s *SeqScan) Open() error {
	s.open, s.nblocks, s.next, s.buf, s.pos, s.PagesRead = true, s.Heap.NumPages(), 0, nil, 0, 0
	return nil
}

// Close ends the scan.
func (s *SeqScan) Close() error { s.open, s.buf = false, nil; return nil }

// Problem 11.5 — SeqScan.Next [Medium]
//
// Return the next visible row of the table, reading it one page at a time through the buffer pool.
//
// "Page-at-a-time" mode: when the buffered rows run out, pin the next page,
// decide visibility for every tuple on it, copy the visible rows out, unpin
// the page — and serve rows from that copy. The scan never keeps a pin
// between Next calls, so a slow consumer cannot block page eviction.
//
//	page 0: [lsn][t1 visible][t2 aborted][t3 visible]  →  buf = [t1, t3]
//	page 1: [lsn][t4 deleted]                          →  buf = []  (keep going)
//	page 2: [lsn][t5 visible]                          →  buf = [t5]
//
// Rules:
//  1. Not open → (nil, false, exec.ErrNotOpen).
//  2. While the buffer is exhausted: if s.next >= s.nblocks the scan is done
//     ((nil, false, nil), forever). Otherwise FetchPage(s.next) from s.Heap.pool,
//     view it with heapPage, and for every (slot, data) of page.Tuples() except
//     the LSN item (slot 0): tuple.DecodeHeader; skip it unless
//     Visible(hdr, s.Snap, s.Clog, s.Mine) (Problem 11.2); otherwise
//     tuple.DeformTuple with s.Heap.Def.Schema. If s.EmitTID, append
//     tidValue(TID{page, slot}) to the row.
//  3. Unpin the page (not dirty) before returning; s.PagesRead++, s.next++.
//     Errors (from the pool or the tuple decoders) are returned after unpinning.
//  4. Rows come out in (page, slot) order.
//
// Example (emp with alice (0,1) committed, bob (0,2) inserted by an aborted
// transaction, carol (0,3) committed then deleted by a committed transaction):
//
//	Collect(SeqScan) → (1,'alice',10,100)             PagesRead 1
//	EmitTID          → (1,'alice',10,100, 1)          ctid (0,1) = 0<<16 | 1
//	5000-row events table (29 pages), Limit 1 above it → PagesRead 1
//
// Constraints: at most one page pinned, only while inside Next.
// Postgres reference: heapgettup_pagemode() / heap_prepare_pagescan() in
// src/backend/access/heap/heapam.c
func (s *SeqScan) Next() (types.Row, bool, error) {
	panic("TODO: Problem 11.5")
}
