package sortagg

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// HashDistinct is SELECT DISTINCT done with a hash table (Postgres: a
// HashAggregate with no aggregates). It streams: a row is returned as soon
// as it is seen for the first time.
type HashDistinct struct {
	Child exec.Iterator // input rows

	seen map[string]bool // Row.HashKey() of every row already returned
}

// NewHashDistinct builds a HashDistinct over child.
func NewHashDistinct(child exec.Iterator) *HashDistinct { return &HashDistinct{Child: child} }

// Open opens the child and clears the seen set.
func (d *HashDistinct) Open() error {
	d.seen = map[string]bool{}
	return d.Child.Open()
}

// Close closes the child.
func (d *HashDistinct) Close() error {
	d.seen = nil
	return d.Child.Close()
}

// Problem 7.12 — HashDistinct.Next [Easy]
//
// Return the next input row whose whole-row value has not been returned before.
//
// Rules:
//  1. Two rows are duplicates when their Row.HashKey() is equal — so NULL
//     matches NULL and 1 matches 1.0 (DISTINCT treats NULLs as equal).
//  2. Return each distinct row the FIRST time it appears; skip later copies.
//     Output keeps input order of first appearances.
//  3. Streaming: pull from the child only until a new row is found.
//  4. Pass child errors through; at end of input return ok=false.
//
// Example:
//
//	(1,'a') (2,'b') (1,'a') (NULL,'x') (NULL,'x') (1,'b')
//	→ (1,'a') (2,'b') (NULL,'x') (1,'b')     after 2 Next calls the child pulled 2 rows
//
// Postgres reference: lookup_hash_entries() in src/backend/executor/nodeAgg.c
func (d *HashDistinct) Next() (types.Row, bool, error) {
	panic("TODO: Problem 7.12")
}

// Unique removes ADJACENT duplicate rows from sorted input (Postgres: the
// Unique node placed above a Sort for SELECT DISTINCT). It needs O(1) memory.
type Unique struct {
	Child exec.Iterator // input, sorted on all columns

	prev    types.Row // last row returned
	hasPrev bool      // prev is valid
}

// NewUnique builds a Unique over child.
func NewUnique(child exec.Iterator) *Unique { return &Unique{Child: child} }

// Open opens the child and forgets the previous row.
func (u *Unique) Open() error {
	u.prev, u.hasPrev = nil, false
	return u.Child.Open()
}

// Close closes the child.
func (u *Unique) Close() error { return u.Child.Close() }

// Problem 7.13 — Unique.Next [Easy]
//
// Return the next input row that differs from the previously returned row (sorted DISTINCT).
//
// Rules:
//  1. A row is a duplicate when it has the same length as the previously
//     returned row and every column is types.Identical (NULL matches NULL).
//  2. Only the previous returned row is remembered: non-adjacent duplicates
//     are NOT removed (that is why the input must be sorted).
//  3. The first row is always returned. Streaming; pass child errors
//     through; at end of input return ok=false.
//
// Example:
//
//	1, 1, 2, NULL, NULL → 1, 2, NULL
//	1, 2, 1             → 1, 2, 1     (unsorted input: duplicates survive)
//
// Postgres reference: ExecUnique() in src/backend/executor/nodeUnique.c
func (u *Unique) Next() (types.Row, bool, error) {
	panic("TODO: Problem 7.13")
}
