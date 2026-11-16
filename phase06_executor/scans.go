package executor

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// SeqScan reads every live row of a table in physical order, optionally
// checking a pushed-down qual (Postgres: SeqScan node + its qual).
type SeqScan struct {
	Table *Table // relation to scan
	Qual  Expr   // filter checked on each row; nil = keep every row

	page  int         // next page to read
	slots []types.Row // the current page's slot array (from ReadPage)
	slot  int         // next slot of slots to look at

	RowsRemoved int // rows rejected by Qual so far
}

// Open resets the scan to the first page.
func (s *SeqScan) Open() error {
	s.page, s.slots, s.slot, s.RowsRemoved = 0, nil, 0, 0
	return nil
}

// Close releases the current page.
func (s *SeqScan) Close() error { s.slots = nil; return nil }

// Problem 6.5 — SeqScan [Easy]
//
// Return the next live row of the table in (page, slot) order that passes the pushed-down qual.
//
// A sequential scan reads the relation page by page from block 0 to the end
// (Postgres: heapgettup_pagemode). Pushing the WHERE clause into the scan
// ("Filter:" under "Seq Scan" in EXPLAIN) avoids handing rejected rows to a
// separate node.
//
// Rules:
//  1. Visit pages 0 .. Table.NumPages()-1 in order and, within a page, slots in
//     increasing order. Skip dead slots (nil rows).
//  2. Read each page with Table.ReadPage exactly once, and only when the previous
//     page is used up — never read ahead (so LIMIT 1 over a 3-rows-per-page table
//     reads one page). Keep the page in s.slots / s.slot, the next page number in s.page.
//  3. If Qual is non-nil, return a row only when Eval(Qual, row) is TRUE; FALSE or
//     NULL increments s.RowsRemoved; any other kind → ErrTypeMismatch.
//  4. When the table is exhausted return (nil, false, nil) — and keep doing so
//     without further ReadPage calls.
//
// Example: 3 rows per page, rows (1..7) with (0,1) deleted, Qual $0 >= 3
//
//	pages: [1, -, 3] [4, 5, 6] [7]   → 3, 4, 5, 6, 7; PageReads = 3; RowsRemoved = 1
//
// Constraints: O(1) extra memory besides the current page.
// Postgres reference: SeqNext() in src/backend/executor/nodeSeqscan.c, heapgettup_pagemode() in heapam.c
func (s *SeqScan) Next() (types.Row, bool, error) {
	panic("TODO: Problem 6.5")
}

// IndexScan walks an index range and fetches each matching row from the heap
// by TID (Postgres: IndexScan node).
type IndexScan struct {
	Index *Index   // index to walk
	Table *Table   // heap the index points into
	Range KeyRange // index condition ("Index Cond:")
	Qual  Expr     // extra filter on fetched rows ("Filter:"); nil = none

	pos     int  // next index entry to examine
	started bool // has the start position been found?
	done    bool // has the range been exhausted?
}

// Open restarts the scan.
func (s *IndexScan) Open() error { s.pos, s.started, s.done = 0, false, false; return nil }

// Close does nothing (nothing to release).
func (s *IndexScan) Close() error { return nil }

// Problem 6.6 — IndexScan [Medium]
//
// Return the next heap row whose indexed key lies in Range, in index order, fetching rows by TID.
//
// An index scan descends the B-tree once to the first key ≥ the lower bound,
// then walks the leaf entries in key order; for every entry it does a random
// heap fetch by TID. That is cheap for a few rows and very expensive for many —
// the trade-off the cost model (Problem 6.14) captures.
//
// Rules:
//  1. On the first Next, find the first index position whose key satisfies the
//     lower bound (Range.Lo; nil = position 0) with a BINARY SEARCH using Index.At
//     — reading every entry is too slow (the tests bound Index.Reads to roughly
//     log2(Len) + number of matches + 2).
//  2. Then examine entries in order. Stop at the first key above the upper bound
//     (Range.Hi) or at the end of the index; from then on return (nil, false, nil)
//     without reading the index or the table again.
//  3. For each in-range entry call Table.Fetch(entry.TID) exactly once. A dead TID
//     (ok=false) is skipped. If Qual is non-nil the row must make it TRUE, else it is
//     skipped (non-bool non-NULL → ErrTypeMismatch).
//  4. Rows come out in index order: by key, ties by TID.
//
// Example: table id = 1..1000 (10 rows/page), index on id, Range 500 ≤ id < 503
//
//	→ rows 500, 501, 502; Table.Fetches = 3; Table.PageReads = 0
//
// Constraints: O(log n + k) index reads for k matching entries.
// Postgres reference: IndexNext() in src/backend/executor/nodeIndexscan.c, _bt_first() in nbtsearch.c
func (s *IndexScan) Next() (types.Row, bool, error) {
	panic("TODO: Problem 6.6")
}

// IndexOnlyScan answers a query from the index alone when the visibility map
// allows it (Postgres: IndexOnlyScan node). Output rows have ONE column: the key.
type IndexOnlyScan struct {
	Index *Index         // index to walk
	Table *Table         // heap, consulted only for pages that are not all-visible
	VM    *VisibilityMap // which heap pages are all-visible
	Range KeyRange       // index condition

	pos     int  // next index entry to examine
	started bool // has the start position been found?
	done    bool // has the range been exhausted?

	HeapFetches int // heap fetches done so far (EXPLAIN ANALYZE "Heap Fetches: N")
}

// Open restarts the scan and resets HeapFetches.
func (s *IndexOnlyScan) Open() error {
	s.pos, s.started, s.done, s.HeapFetches = 0, false, false, 0
	return nil
}

// Close does nothing.
func (s *IndexOnlyScan) Close() error { return nil }

// Problem 6.7 — IndexOnlyScan [Medium]
//
// Return the next in-range key as a one-column row, touching the heap only for pages that are not all-visible.
//
// An index entry says nothing about whether its row version is visible to
// this snapshot (MVCC info lives only in the heap tuple). Postgres can still
// skip the heap when the visibility map says the TID's page is all-visible:
// then every tuple on it is visible to everyone. Otherwise it must fetch the
// heap tuple to check — those are the "Heap Fetches" in EXPLAIN ANALYZE, and
// why a freshly loaded, never-VACUUMed table gets slow index-only scans.
//
// Rules:
//  1. Find the start and walk the range exactly like Problem 6.6 (binary search on
//     the lower bound, stop at the first key above the upper bound, stay done).
//  2. For each in-range entry call VM.AllVisible(entry.TID.Page) exactly once.
//     - all-visible → return types.Row{entry.Key} without touching the table;
//     - otherwise → Table.Fetch(entry.TID) and increment s.HeapFetches; if the
//     tuple is dead (ok=false) skip the entry, else return types.Row{entry.Key}.
//  3. The output always comes from the index entry, never from the heap row.
//
// Example: ids 1..12 at 4 rows/page (pages 0,1,2), pages 0 and 2 all-visible,
// id 7 (TID (1,2)) deleted, Range 3 ≤ id ≤ 10
//
//	→ (3),(4),(5),(6),(8),(9),(10); HeapFetches = 4 (ids 5..8 live on page 1)
//
// Constraints: O(log n + k) index reads; no heap access for all-visible pages.
// Postgres reference: IndexOnlyNext() in src/backend/executor/nodeIndexonlyscan.c
func (s *IndexOnlyScan) Next() (types.Row, bool, error) {
	panic("TODO: Problem 6.7")
}
