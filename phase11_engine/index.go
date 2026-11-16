package engine

import (
	"fmt"
	"slices"

	btree "github.com/natthphong/daTarbase/phase05_btree"
	executor "github.com/natthphong/daTarbase/phase06_executor"
	txn "github.com/natthphong/daTarbase/phase09_txn"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Index is an open B+Tree index (phase 5). Keys are phase-1 memcomparable
// encodings (EncodeKey) of the key columns; every leaf entry also carries a
// payload — a phase-1 tuple of the key columns followed by the INCLUDE
// columns — which is what an Index Only Scan returns.
//
// Like a Postgres index, it has one entry per heap tuple VERSION (live,
// dead or not yet committed); scans recheck visibility in the heap, VACUUM
// removes the entries of dead versions.
type Index struct {
	Def           *IndexDef
	Heap          *TableHeap
	KeyCols       []int        // table column positions of the key columns
	InclCols      []int        // table column positions of the INCLUDE columns
	PayloadSchema types.Schema // key columns then INCLUDE columns, all nullable, named "table.col"
	Fanout        int          // btree MaxItems
	Tree          *btree.Tree  // built by Build (Problem 11.9)
}

// newIndex resolves the column names of def against the heap's schema.
func newIndex(def *IndexDef, h *TableHeap, fanout int) (*Index, error) {
	ix := &Index{Def: def, Heap: h, Fanout: fanout}
	pos := func(name string) (int, error) {
		i := h.Def.Schema.Index(name)
		if i < 0 {
			return 0, fmt.Errorf("%w: %q in table %q", ErrNoSuchColumn, name, h.Def.Name)
		}
		c := h.Def.Schema.Columns[i]
		ix.PayloadSchema.Columns = append(ix.PayloadSchema.Columns, types.Column{Name: h.Def.Name + "." + c.Name, Kind: c.Kind})
		return i, nil
	}
	for _, c := range def.Columns {
		i, err := pos(c)
		if err != nil {
			return nil, err
		}
		ix.KeyCols = append(ix.KeyCols, i)
	}
	for _, c := range def.Include {
		i, err := pos(c)
		if err != nil {
			return nil, err
		}
		ix.InclCols = append(ix.InclCols, i)
	}
	return ix, nil
}

// Columns is the output column list of an Index Only Scan: PayloadSchema's names.
func (ix *Index) Columns() []string {
	out := make([]string, len(ix.PayloadSchema.Columns))
	for i, c := range ix.PayloadSchema.Columns {
		out[i] = c.Name
	}
	return out
}

// covers reports whether every given table column is stored in the payload.
func (ix *Index) covers(cols []int) bool {
	for _, c := range cols {
		if !slices.Contains(ix.KeyCols, c) && !slices.Contains(ix.InclCols, c) {
			return false
		}
	}
	return true
}

// Problem 11.8 — Index.Entry [Easy]
//
// Build the B+Tree leaf entry for one heap row: memcomparable key, heap TID, and the covering payload.
//
//	row (id 7, name 'gus', dept 20, salary 95)  index (dept) INCLUDE (name)
//	→ Item{Key: EncodeKey((20)), TID: tid, Payload: FormTuple((20, 'gus'))}
//
// Rules:
//  1. keyVals = the row's values at ix.KeyCols, in order.
//     Key = tuple.EncodeKey(keyVals, nil) (all columns ascending).
//  2. Payload = tuple.FormTuple(ix.PayloadSchema, keyVals followed by the values
//     at ix.InclCols, xmin 0). NULLs are allowed everywhere.
//  3. Item{Key, TID: tid, Payload}. Errors from the encoders are returned as-is.
//  4. Do not modify row.
//
// Example (emp_dept on (dept) INCLUDE (name); dept int4):
//
//	Entry((7,'gus',20,95), (0,7)) → Key 01 80 00 00 14, TID (0,7),
//	                                Payload decodes (DeformTuple with PayloadSchema) to (20,'gus')
//	Entry((8,'hal',NULL,90), (0,8)) → Key 02 (NULL sorts last), Payload (NULL,'hal')
//
// Constraints: two encodings, no other allocation.
// Postgres reference: FormIndexDatum() in src/backend/catalog/index.c, index_form_tuple() in indextuple.c
func (ix *Index) Entry(row types.Row, tid types.TID) (btree.Item, error) {
	panic("TODO: Problem 11.8")
}

// Problem 11.9 — Index.Build [Medium]
//
// Build the index from every tuple version already in the heap, the way CREATE INDEX does: collect, sort, bulk-load.
//
// CREATE INDEX does not insert entries one by one: it scans the heap, sorts
// the entries and builds the tree bottom-up (phase 5 BulkLoad), writing every
// node once. Open uses the same path to rebuild indexes after a restart (our
// B+Trees live in memory).
//
// Rules:
//  1. Visit pages 0 .. ix.Heap.NumPages()-1 through ix.Heap.pool (FetchPage,
//     heapPage, unpin clean), and every live line pointer (page.Tuples()) except
//     the LSN item in slot 0.
//  2. Take EVERY version, whatever its visibility: committed, deleted,
//     in progress or aborted. (Index scans recheck the heap; VACUUM removes
//     dead entries. Postgres' CREATE INDEX may skip versions that are dead to
//     everyone — we don't.)
//  3. Deform each tuple with the heap's schema, make its entry with Entry
//     (Problem 11.8) at TID (page, slot).
//  4. Sort the entries with slices.SortFunc(items, btree.CompareItems), then
//     ix.Tree, err = btree.BulkLoad(btree.NewNodeStore(), ix.Fanout, items, 0).
//  5. On error return it and leave ix.Tree unchanged.
//
// Example (emp with 3 rows on page 0, one of them deleted, Fanout 4):
//
//	Build() → ix.Tree.Validate() == nil, Height 1, leaf entries in (key, TID) order:
//	          (10,(0,1)) (10,(0,3)) (20,(0,2))   — the deleted row's entry is there too
//	50 employees, Fanout 4 → 50 entries, every leaf holds 3 (fillfactor 90) except the last
//	empty table → one empty root leaf
//
// Constraints: one pass over the heap, one sort, each node written once.
// Postgres reference: btbuild() / _bt_spools_heapscan() in src/backend/access/nbtree/nbtsort.c
func (ix *Index) Build() error {
	panic("TODO: Problem 11.9")
}

// Problem 11.11 — KeyBounds [Medium]
//
// Turn "leading index column <op> v" into the B+Tree range [lo, hi] over encoded keys that holds exactly the matching entries.
//
// Index keys are memcomparable bytes (phase 1 EncodeKey) and may contain more
// columns after the leading one, so a bound on the leading value is a bound on
// a key PREFIX. Two helpers make that exact:
//
//	e       = EncodeKey((v), nil)            every key whose leading value is v starts with e
//	succ(e) = e without its trailing 0xFF bytes, last byte + 1
//	          → the smallest byte string greater than every string that starts with e
//	NULLs   = encoded with tag 0x02 (KeyTagNull), after every non-NULL value
//	          (tag 0x01) — a comparison is never true for NULL, so ranges must stop before them
//
//	op   lo            hi
//	=    Incl(e)       Excl(succ(e))
//	<    NoBound()     Excl(e)
//	<=   NoBound()     Excl(succ(e))
//	>    Incl(succ(e)) Excl([]byte{KeyTagNull})
//	>=   Incl(e)       Excl([]byte{KeyTagNull})
//
// Rules:
//  1. op must be OpEQ, OpLT, OpLE, OpGT or OpGE; OpNE (or anything else) →
//     ErrBadOperator. v must not be NULL → ErrBadOperator ("x = NULL" is never true;
//     the planner keeps such conditions out of the index).
//  2. Encode v with tuple.EncodeKey (its error is returned as-is) and build the
//     bounds from the table above. Never modify e when computing succ(e).
//  3. v already has the column's kind (the planner coerces it).
//
// Example (int4 column):
//
//	KeyBounds(OpEQ, 10)  → [01 80 00 00 0a, 01 80 00 00 0b)
//	KeyBounds(OpGT, 255) → [01 80 00 01, 02)                 (succ drops the trailing ff)
//	KeyBounds(OpLE, 'b') → (-inf, 01 62 00 02)               (text 'b' = 01 62 00 01)
//	on an index (dept, name): OpEQ 10 matches (10,'alice') and (10,'zed'), not (11,'a')
//
// Constraints: O(len(e)).
// Postgres reference: _bt_first() strategy handling in src/backend/access/nbtree/nbtsearch.c,
// make_greater_string() in src/backend/utils/adt/like_support.c
func KeyBounds(op executor.CmpOp, v types.Value) (lo, hi btree.Bound, err error) {
	panic("TODO: Problem 11.11")
}

// IndexScan returns the heap rows whose index key lies in [Lo, Hi] and that
// are visible to the snapshot (Postgres: Index Scan). It is an exec.Iterator.
type IndexScan struct {
	Index   *Index
	Lo, Hi  btree.Bound // key range (see KeyBounds)
	Snap    *txn.Snapshot
	Clog    *txn.Clog
	Mine    txn.CurrentXids
	EmitTID bool // append the TID as an extra int8 "ctid" column

	cur  *btree.Cursor
	open bool

	HeapFetches int // heap tuples fetched since Open
}

// Open positions a fresh B+Tree cursor (lazily: no node is read yet).
func (s *IndexScan) Open() error {
	s.cur, s.open, s.HeapFetches = s.Index.Tree.Scan(s.Lo, s.Hi, false), true, 0
	return nil
}

// Close ends the scan.
func (s *IndexScan) Close() error { s.open, s.cur = false, nil; return nil }

// Problem 11.12 — IndexScan.Next [Medium]
//
// Return the next visible heap row whose index key is in range: walk the B+Tree cursor, fetch each TID from the heap, recheck visibility.
//
// The index knows keys and TIDs, but not whether a version is visible — that
// lives in the heap tuple's xmin/xmax. So every entry costs a heap fetch:
//
//	cursor → (key 10, (0,1)) → heap (0,1): xmin committed       → return (1,'alice',10)
//	       → (key 10, (0,3)) → heap (0,3): xmax committed       → skip
//	       → (key 10, (2,4)) → heap (2,4): inserted by aborted  → skip
//
// Rules:
//  1. Not open → (nil, false, exec.ErrNotOpen).
//  2. Loop: item, ok := s.cur.Next(); !ok → (nil, false, nil).
//  3. Fetch the heap tuple at item.TID through s.Index.Heap.pool (pin,
//     heapPage, GetTuple, unpin clean); count s.HeapFetches++ for every entry
//     fetched. If the slot holds no live tuple (GetTuple fails: it was
//     vacuumed), skip the entry.
//  4. Decode the header; skip the entry unless Visible(hdr, s.Snap, s.Clog,
//     s.Mine). Otherwise DeformTuple with the heap's schema, append
//     tidValue(item.TID) if s.EmitTID, and return the row.
//  5. Rows come out in index order (key, then TID). Errors are returned after
//     unpinning.
//
// Example (emp_dept on dept; rows alice 10, bob 20, carol 10, dave 20, erin 30,
// frank NULL, committed; carol then deleted (committed); gus 10 inserted by a
// transaction that rolled back; Lo/Hi = KeyBounds(OpEQ, 10)):
//
//	Collect → (1,'alice',10,100)   HeapFetches 3 (alice, carol, gus)
//	KeyBounds(OpGE, 20) → bob, dave, erin (20, 20, 30 — frank's NULL is outside the range)
//	EmitTID with KeyBounds(OpEQ, 20) → (2,'bob',20,80, 2), (4,'dave',20,90, 4)
//
// Constraints: one heap page pinned at a time; no pin held between calls.
// Postgres reference: IndexNext() in src/backend/executor/nodeIndexscan.c,
// heapam_index_fetch_tuple() in heapam_handler.c
func (s *IndexScan) Next() (types.Row, bool, error) {
	panic("TODO: Problem 11.12")
}

// IndexOnlyScan answers from the index payload alone when the visibility map
// allows it (Postgres: Index Only Scan). It is an exec.Iterator whose rows have
// the columns of Index.PayloadSchema (key columns, then INCLUDE columns).
type IndexOnlyScan struct {
	Index  *Index
	Lo, Hi btree.Bound
	Snap   *txn.Snapshot
	Clog   *txn.Clog
	Mine   txn.CurrentXids

	cur  *btree.Cursor
	open bool

	HeapFetches int // entries whose page was not all-visible ("Heap Fetches" in EXPLAIN ANALYZE)
}

// Open positions a fresh B+Tree cursor.
func (s *IndexOnlyScan) Open() error {
	s.cur, s.open, s.HeapFetches = s.Index.Tree.Scan(s.Lo, s.Hi, false), true, 0
	return nil
}

// Close ends the scan.
func (s *IndexOnlyScan) Close() error { s.open, s.cur = false, nil; return nil }

// Problem 11.13 — IndexOnlyScan.Next [Medium]
//
// Return the next visible entry's payload, skipping the heap for pages the visibility map marks all-visible.
//
// An index entry may point at a dead or uncommitted version, so in general
// the heap must be checked. The visibility map (one bit per heap page, set by
// VACUUM — Problem 11.19 — and cleared by every insert/delete on the page)
// says "every tuple on this page is visible to everyone": then the payload
// can be returned without touching the heap.
//
// Rules:
//  1. Not open → (nil, false, exec.ErrNotOpen).
//  2. Loop over s.cur.Next(); exhausted → (nil, false, nil).
//  3. If s.Index.Heap.vm[item.TID.Page] is true: return the payload decoded
//     with tuple.DeformTuple(s.Index.PayloadSchema, item.Payload). No heap
//     access, no HeapFetches.
//  4. Otherwise s.HeapFetches++ and read the heap tuple's header at item.TID
//     (pin, heapPage, GetTuple, DecodeHeader, unpin clean). A slot without a
//     live tuple → skip. Not Visible → skip. Visible → return the decoded payload.
//  5. Output order = index order. Errors are returned as-is.
//
// Example (index on (dept) INCLUDE (name); alice 10 and bob 20 on page 0,
// carol 10 (deleted, committed) and dave 10 on page 1; KeyBounds(OpEQ, 10)):
//
//	vm = {0: true}          → (10,'alice') (10,'dave')                 HeapFetches 2 (carol, dave)
//	vm = {}                 → (10,'alice') (10,'dave')                 HeapFetches 3
//	vm = {0: true, 1: true} → (10,'alice') (10,'carol') (10,'dave')    HeapFetches 0
//	                          (the map is trusted blindly — a wrong bit returns a deleted row)
//
// Constraints: never fetch the heap for an all-visible page.
// Postgres reference: IndexOnlyNext() in src/backend/executor/nodeIndexonlyscan.c
func (s *IndexOnlyScan) Next() (types.Row, bool, error) {
	panic("TODO: Problem 11.13")
}
