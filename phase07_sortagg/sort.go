package sortagg

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// Sort is an in-memory ORDER BY operator (Postgres: Sort node with a
// tuplesort that fits in work_mem). Open buffers and sorts the whole input;
// Next then hands the rows out one at a time.
type Sort struct {
	Child exec.Iterator // input rows
	Keys  []SortKey     // ORDER BY keys

	rows []types.Row // sorted output, filled by Open
	pos  int         // next index of rows to return
}

// NewSort builds a Sort over child.
func NewSort(child exec.Iterator, keys ...SortKey) *Sort { return &Sort{Child: child, Keys: keys} }

// Problem 7.2 — Sort.Open [Easy]
//
// Open the child, read every row from it and sort them in memory, stably.
//
// Sort is a "blocking" operator: it cannot return its first row before it
// has seen the last input row. Open does all the work; the provided Next
// returns s.rows[s.pos] and advances s.pos; the provided Close closes the child.
//
// Rules:
//  1. Open the child (return its error), then call Next until ok=false.
//     Return the first child error you see.
//  2. Store the rows in s.rows ordered by CompareRows(·,·,s.Keys) and reset s.pos to 0.
//  3. The sort must be STABLE: rows that compare equal keep their input order.
//  4. Do NOT close the child here (Close does that).
//  5. Empty input → Open succeeds and Next immediately reports no rows.
//
// Example (rows are (id, dept, salary)):
//
//	input: (1,'ops',80) (2,'eng',100) (3,'ops',90) (4,'eng',120) (5,'eng',100)
//	Keys [Asc(1)]          → ids 2,4,5,1,3   (equal depts keep input order)
//	Keys [Asc(1), Desc(2)] → ids 4,2,5,3,1
//
// Constraints: O(n log n) comparisons.
// Postgres reference: ExecSort() in src/backend/executor/nodeSort.c
func (s *Sort) Open() error {
	panic("TODO: Problem 7.2")
}

// Next returns the next sorted row.
func (s *Sort) Next() (types.Row, bool, error) {
	if s.pos >= len(s.rows) {
		return nil, false, nil
	}
	r := s.rows[s.pos]
	s.pos++
	return r, true, nil
}

// Close releases the buffered rows and closes the child.
func (s *Sort) Close() error {
	s.rows = nil
	return s.Child.Close()
}

// topNEntry is a candidate row of TopN plus its 0-based position in the input
// (used to break ties so that earlier input rows win).
type topNEntry struct {
	Row types.Row
	Seq int
}

// TopN is ORDER BY ... LIMIT N done with a bounded heap (Postgres:
// tuplesort's "bounded" mode, chosen when a Limit sits on top of a Sort).
// Only the best N rows seen so far are kept in memory.
type TopN struct {
	Child exec.Iterator // input rows
	Keys  []SortKey     // ORDER BY keys
	N     int           // LIMIT

	heap []topNEntry // at most N candidates; use as a heap with the WORST candidate at the root
	out  []types.Row // final sorted rows, filled by Open
	pos  int         // next index of out to return
	Peak int         // largest len(heap) ever observed during Open (memory high-water mark)
}

// NewTopN builds a TopN over child.
func NewTopN(child exec.Iterator, n int, keys ...SortKey) *TopN {
	return &TopN{Child: child, Keys: keys, N: n}
}

// Problem 7.3 — TopN.Open [Medium]
//
// Compute ORDER BY Keys LIMIT N while holding at most N rows in memory.
//
// A full sort of a million rows to return 10 is wasteful. Instead keep the
// best N rows seen so far in a heap whose root is the WORST of them. Each new
// row is compared with the root: if it is better, it replaces the root and
// the heap is fixed up; otherwise it is thrown away immediately.
//
// Rules:
//  1. Open the child and read it to the end (the whole input must be
//     consumed — any row could belong in the answer). Do not close it.
//  2. Ordering is CompareRows(·,·,t.Keys), ties broken by input position:
//     the row that came EARLIER in the input wins. So the result is exactly
//     the first N rows of a stable sort.
//  3. Keep candidates in t.heap (with their input position in Seq). It must
//     never hold more than N entries. After every append to t.heap, set
//     t.Peak = max(t.Peak, len(t.heap)).
//  4. When finished, store the kept rows in t.out in final sorted order
//     (best first) and reset t.pos to 0. The provided Next/Close serve t.out
//     and close the child.
//  5. N <= 0 → the result is empty and the child must not be read at all
//     (open it, but pull 0 rows).
//  6. Fewer than N input rows → return all of them, sorted.
//
// Example: input rows (i, i*37 % 100) for i = 0..999 (each value 0..99 occurs 10 times).
//
//	Keys [Asc(1)],  N=5 → (0,0) (100,0) (200,0) (300,0) (400,0)   Peak=5, child pulled 1000
//	Keys [Desc(1)], N=3 → (27,99) (127,99) (227,99)
//
// Hint: container/heap with Less = "is worse than", comparing (keys, Seq).
// Constraints: O(n log N) time, O(N) memory.
// Postgres reference: make_bounded_heap() / puttuple_common() in src/backend/utils/sort/tuplesort.c
func (t *TopN) Open() error {
	panic("TODO: Problem 7.3")
}

// Next returns the next of the top N rows.
func (t *TopN) Next() (types.Row, bool, error) {
	if t.pos >= len(t.out) {
		return nil, false, nil
	}
	r := t.out[t.pos]
	t.pos++
	return r, true, nil
}

// Close releases the rows and closes the child.
func (t *TopN) Close() error {
	t.heap, t.out = nil, nil
	return t.Child.Close()
}
