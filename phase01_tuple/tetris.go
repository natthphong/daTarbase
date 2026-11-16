package tuple

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 1.15 — ColumnTetris [Medium]
//
// Reorder a table's columns to minimize alignment padding and report the bytes saved per row.
//
// Declaring (bool, int8, bool, int8) wastes 14 bytes per row in padding:
//
//	original: [b][7 pad][int8 int8 ....][b][7 pad][int8 ........]   32 bytes
//	reordered:[int8 ........][int8 ........][b][b]                  18 bytes
//
// Postgres never reorders columns for you, so DBAs play "column tetris".
// Putting text last also keeps the fixed columns' offsets cacheable
// (Problem 1.13).
//
// Rules:
//  1. New order: fixed-width columns first, sorted by AttAlign descending
//     (8, then 4, then 1); ties keep their original relative order (stable).
//     Then all text columns, in their original relative order.
//  2. order[k] = original index of the column placed at position k.
//  3. Cost model: a row where every value is non-NULL and every text value is
//     the empty string, i.e. types.Text("") (a 1-byte short varlena, never
//     aligned); fixed-width values can be anything non-NULL. Measure the
//     size of that row with ComputeTupleSize in the original order and in the
//     new order; saved = original - new (always >= 0).
//  4. An empty schema → empty (non-nil) order and saved 0.
//
// Example:
//
//	(id int4, active bool, score float8, name text, big int8)
//	  → order [2 4 0 1 3], saved 10   (48 → 38 bytes)
//	(b1 bool, i1 int8, b2 bool, i2 int8) → order [1 3 0 2], saved 14
//	(t1 text, n int4, t2 text, f bool)   → order [1 3 0 2], saved 3
//
// Postgres reference: none (Postgres keeps declaration order; see "column
// tetris" discussions and pg_attribute.attalign).
func ColumnTetris(schema types.Schema) ([]int, int) {
	panic("TODO: Problem 1.15")
}
