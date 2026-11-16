// Package join implements the three physical join algorithms of a relational
// executor — nested loop, hash and merge join — for every SQL join type
// (INNER, LEFT, RIGHT, FULL, SEMI, ANTI), plus Grace hash partitioning,
// an index nested loop, a join-size estimator and a Selinger-style join-order
// search.
//
// Everything in this file except Problem 8.1 is PROVIDED.
//
// Conventions used by every join in this phase:
//
//   - The output row of INNER / LEFT / RIGHT / FULL is left columns ++ right
//     columns (see Concat). The side with no match is padded with NULLs
//     (see NullRow), using LeftWidth / RightWidth of the JoinSpec.
//   - SEMI and ANTI output the left row only (exactly the input row).
//   - The join condition is the conjunction of the equi keys in Keys and the
//     optional Extra predicate evaluated on the combined row. Together they
//     are the ON clause: a pair of rows "matches" only if both hold.
//   - SQL semantics: NULL = anything is not true, so a row with a NULL in any
//     of its key columns never matches anything.
package join

import (
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/natthphong/daTarbase/pkg/types"
)

// JoinType is the logical kind of join (Postgres: JoinType in nodes.h).
type JoinType int

const (
	Inner JoinType = iota // rows that match
	Left                  // INNER + unmatched left rows padded with NULLs on the right
	Right                 // INNER + unmatched right rows padded with NULLs on the left
	Full                  // INNER + unmatched rows of both sides
	Semi                  // left rows that have at least one match (EXISTS), output once
	Anti                  // left rows that have no match (NOT EXISTS)
)

func (t JoinType) String() string {
	switch t {
	case Inner:
		return "INNER"
	case Left:
		return "LEFT"
	case Right:
		return "RIGHT"
	case Full:
		return "FULL"
	case Semi:
		return "SEMI"
	case Anti:
		return "ANTI"
	}
	return fmt.Sprintf("JoinType(%d)", int(t))
}

// KeyPair is one equality condition: left[Left] = right[Right].
type KeyPair struct {
	Left  int // column index in the left row
	Right int // column index in the right row
}

// JoinSpec describes one join: its type and its ON clause.
type JoinSpec struct {
	Type       JoinType                      // which join to compute
	Keys       []KeyPair                     // equi-join keys; empty = no key condition (cross join)
	Extra      func(types.Row) (bool, error) // optional extra ON-clause predicate on Concat(left, right); nil = always true
	LeftWidth  int                           // number of columns in a left row (for NULL padding)
	RightWidth int                           // number of columns in a right row (for NULL padding)
}

// LeftCols returns the left column of every key pair, in order.
func (s JoinSpec) LeftCols() []int {
	out := make([]int, len(s.Keys))
	for i, k := range s.Keys {
		out[i] = k.Left
	}
	return out
}

// RightCols returns the right column of every key pair, in order.
func (s JoinSpec) RightCols() []int {
	out := make([]int, len(s.Keys))
	for i, k := range s.Keys {
		out[i] = k.Right
	}
	return out
}

var (
	// ErrUnsupportedJoin is returned when a function is given a JoinType it does not implement.
	ErrUnsupportedJoin = errors.New("join: unsupported join type")
	// ErrBadConfig is returned for invalid Grace hash join settings.
	ErrBadConfig = errors.New("join: bad config")
	// ErrBadInput is returned by the planner problems for malformed input.
	ErrBadInput = errors.New("join: bad input")
)

// Concat returns a NEW row: l's values followed by r's values.
func Concat(l, r types.Row) types.Row {
	out := make(types.Row, 0, len(l)+len(r))
	out = append(out, l...)
	return append(out, r...)
}

// NullRow returns a row of n NULLs.
func NullRow(n int) types.Row {
	out := make(types.Row, n)
	for i := range out {
		out[i] = types.Null()
	}
	return out
}

// KeyOf returns a string identifying the values of row at cols (usable as a
// Go map key; 1 and 1.0 give the same key). hasNull is true if any of those
// values is NULL — such a row can never match in an equi-join.
func KeyOf(row types.Row, cols []int) (key string, hasNull bool) {
	var b []byte
	for i, c := range cols {
		v := row[c]
		if v.IsNull() {
			hasNull = true
		}
		if i > 0 {
			b = append(b, 0)
		}
		b = append(b, v.HashKey()...)
	}
	return string(b), hasNull
}

// CompareKeys compares the key of left row l (columns lcols) with the key of
// right row r (columns rcols), column by column, in the order merge join
// inputs are sorted: ascending, NULLS LAST, and NULL "equals" NULL for
// ordering purposes. Returns -1, 0 or +1.
//
// A result of 0 does NOT mean the rows match: if the keys contain NULLs
// they are equal for sorting but never equal in SQL.
func CompareKeys(l types.Row, lcols []int, r types.Row, rcols []int) int {
	for i := range lcols {
		a, b := l[lcols[i]], r[rcols[i]]
		switch {
		case a.IsNull() && b.IsNull():
			continue
		case a.IsNull():
			return 1
		case b.IsNull():
			return -1
		}
		if c := types.Compare(a, b); c != 0 {
			return c
		}
	}
	return 0
}

// PartitionOf maps a join key (as returned by KeyOf) to a partition number in
// [0, n) at the given recursion level. Different levels use different hash
// seeds, so a partition that is split again at level+1 spreads its keys over
// new partitions (Postgres: ExecHashGetBucketAndBatch uses different hash bits).
func PartitionOf(key string, level, n int) int {
	h := fnv.New32a()
	h.Write([]byte{byte(level), byte(level >> 8), 0x5a})
	h.Write([]byte(key))
	x := h.Sum32()
	// murmur3 finalizer: FNV's low bits alone mix poorly.
	x ^= x >> 16
	x *= 0x85ebca6b
	x ^= x >> 13
	x *= 0xc2b2ae35
	x ^= x >> 16
	return int(x % uint32(n))
}

// FakeIndex is an in-memory stand-in for a B-tree index on the right input
// of an index nested loop join. It maps a key (the values of Cols) to every
// row with that key, in insertion order, and counts lookups.
type FakeIndex struct {
	Cols    []int                  // indexed columns of the right rows
	entries map[string][]types.Row // KeyOf(row, Cols) → rows, in insertion order
	Lookups int                    // number of Lookup calls (Postgres: index scans started)
}

// NewFakeIndex indexes rows on cols. Rows with a NULL in any indexed column
// are stored too, but no Lookup can ever return them.
func NewFakeIndex(rows []types.Row, cols []int) *FakeIndex {
	ix := &FakeIndex{Cols: cols, entries: map[string][]types.Row{}}
	for _, r := range rows {
		k, hasNull := KeyOf(r, cols)
		if hasNull {
			continue
		}
		ix.entries[k] = append(ix.entries[k], r)
	}
	return ix
}

// Lookup returns the rows whose indexed columns equal key (one value per
// indexed column, in Cols order), in insertion order. A key containing NULL
// returns nothing. Every call counts as one lookup.
func (ix *FakeIndex) Lookup(key []types.Value) []types.Row {
	ix.Lookups++
	parts := make(types.Row, len(key))
	copy(parts, key)
	idx := make([]int, len(key))
	for i := range idx {
		idx[i] = i
	}
	k, hasNull := KeyOf(parts, idx)
	if hasNull {
		return nil
	}
	return ix.entries[k]
}

// Problem 8.1 — JoinSpec.Match [Easy]
//
// Decide whether a left row and a right row satisfy the ON clause of the join.
//
// Every join algorithm in this phase eventually asks the same question about
// a candidate pair: "do these two rows join?". Hash and merge join only use
// the equi keys to FIND candidates quickly; they still re-check the full
// condition with this function (Postgres re-checks the hash clauses after a
// hash-value match, because different values can share a hash).
//
// Rules:
//  1. Check the key pairs in order. If, for any pair, left[k.Left] or
//     right[k.Right] is NULL, the result is false (SQL: NULL = x is not true,
//     even NULL = NULL).
//  2. If, for any pair, types.Compare(left[k.Left], right[k.Right]) != 0, the
//     result is false. (Test data never compares incomparable kinds.)
//  3. No key pairs at all means the key condition is trivially true.
//  4. If the keys hold and s.Extra != nil, return s.Extra(Concat(left, right))
//     — its bool and its error. If s.Extra is nil, return true.
//  5. Extra must NOT be called when the keys already failed (it may be
//     expensive or have side effects).
//  6. Do not modify the input rows.
//
// Example (employees (id, name, dept_id) ⨝ departments (id, name) ON e.dept_id = d.id,
// Keys = [{Left: 2, Right: 0}]):
//
//	(1,'alice',10)   vs (10,'eng')        → true
//	(5,'erin',40)    vs (10,'eng')        → false
//	(4,'dave',NULL)  vs (NULL,'unassigned') → false   (NULL never equals NULL)
//	with Extra = "d.name <> 'sales-emea'":
//	(2,'bob',20)     vs (20,'sales-emea') → false     (Extra sees (2,'bob',20,20,'sales-emea'))
//
// Constraints: O(len(Keys)) + one Extra call at most.
// Postgres reference: ExecQual() on joinqual/otherqual in src/backend/executor/nodeNestloop.c
func (s JoinSpec) Match(left, right types.Row) (bool, error) {
	panic("TODO: Problem 8.1")
}
