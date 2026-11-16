package sortagg

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// IncrementalSort sorts by Keys when the input is already sorted by the
// first Presorted keys (Postgres 13+: Incremental Sort node, e.g. ORDER BY a, b
// over an index on a). Only one prefix group is held in memory at a time,
// and the first rows come out long before the input ends.
type IncrementalSort struct {
	Child     exec.Iterator // input, already sorted by Keys[:Presorted]
	Keys      []SortKey     // full ORDER BY
	Presorted int           // number of leading Keys the input is sorted by (0..len(Keys))

	buf     []types.Row // current group, sorted, being handed out
	pos     int         // next index of buf to return
	pending types.Row   // first row of the next group, already pulled
	hasPend bool        // pending is valid
	done    bool        // child exhausted
}

// NewIncrementalSort builds an IncrementalSort.
func NewIncrementalSort(child exec.Iterator, presorted int, keys ...SortKey) *IncrementalSort {
	return &IncrementalSort{Child: child, Keys: keys, Presorted: presorted}
}

// Open opens the child and resets state.
func (s *IncrementalSort) Open() error {
	s.buf, s.pos, s.pending, s.hasPend, s.done = nil, 0, nil, false, false
	return s.Child.Open()
}

// Close closes the child.
func (s *IncrementalSort) Close() error {
	s.buf = nil
	return s.Child.Close()
}

// Problem 7.14 — IncrementalSort.Next [Medium]
//
// Return rows ordered by all Keys, sorting one presorted-prefix group at a time.
//
//	input sorted by a:          (1,3,'x') (1,1,'y') (1,2,'z') | (2,9,'p') (2,8,'q') | (3,1,'r')
//	ORDER BY a, b, Presorted=1: group a=1 sorted by b → (1,1,'y') (1,2,'z') (1,3,'x'), ...
//
// Rules:
//  1. A prefix group is a maximal run of consecutive input rows for which
//     CompareRows(first, row, s.Keys[:s.Presorted]) == 0 (two NULLs compare equal).
//  2. When s.buf is used up, build the next group: start with s.pending (if
//     any) and pull rows until one belongs to a different group — store that
//     row in s.pending — or the child ends. Stable-sort the group with
//     CompareRows(·,·,s.Keys), put it in s.buf, reset s.pos, and return rows from it.
//  3. Streaming: never read more than one row past the current group.
//  4. Presorted == 0 → the whole input is one group (a plain full sort).
//  5. Pass child errors through; when everything is returned, ok=false.
//
// Example: the input above → (1,1,'y') (1,2,'z') (1,3,'x') (2,8,'q') (2,9,'p') (3,1,'r');
// child.Pulled after the first Next = 4 (3 rows of a=1 plus the pending a=2 row).
//
// Postgres reference: ExecIncrementalSort() in src/backend/executor/nodeIncrementalSort.c
func (s *IncrementalSort) Next() (types.Row, bool, error) {
	panic("TODO: Problem 7.14")
}
