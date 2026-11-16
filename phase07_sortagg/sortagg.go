// Package sortagg implements the "blocking" side of the executor: ORDER BY
// (in-memory sort, top-N heapsort, external merge sort, incremental sort),
// GROUP BY (hash and sorted/streaming aggregation, HAVING, DISTINCT
// aggregates), DISTINCT / Unique, and window functions.
package sortagg

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// SortKey is one ORDER BY item: a column position plus direction and NULL
// placement (Postgres: SortGroupClause / SortSupportData).
//
// NullsFirst means "NULL rows come first in the output", independent of
// Desc. Use Asc / Desc to get Postgres' defaults.
type SortKey struct {
	Col        int  // column index in the row
	Desc       bool // true = DESC (largest first)
	NullsFirst bool // true = NULLS FIRST, false = NULLS LAST
}

// Key builds a SortKey with Postgres' default NULL placement:
// ASC → NULLS LAST, DESC → NULLS FIRST (NULL sorts as if larger than every value).
func Key(col int, desc bool) SortKey { return SortKey{Col: col, Desc: desc, NullsFirst: desc} }

// Asc is ORDER BY col ASC (NULLS LAST).
func Asc(col int) SortKey { return Key(col, false) }

// Desc is ORDER BY col DESC (NULLS FIRST).
func Desc(col int) SortKey { return Key(col, true) }

// Problem 7.1 — CompareRows [Medium]
//
// Compare two rows under a list of ORDER BY keys, honouring ASC/DESC and NULLS FIRST/LAST.
//
// This is the comparator every sort in this phase is built on (Postgres:
// the SortSupport comparator invoked per key by tuplesort).
//
// Rules:
//  1. Return a negative number if a sorts before b, positive if after, 0 if
//     they are equal on every key. Only the sign is checked.
//  2. Keys are compared left to right; the first key that is not equal
//     decides. No keys (nil or empty) → 0.
//  3. Two NULLs are equal for ordering purposes (move on to the next key).
//  4. Exactly one NULL: if key.NullsFirst, the NULL row sorts first,
//     otherwise last. This does NOT depend on key.Desc.
//  5. Two non-NULLs: use types.Compare; if key.Desc, invert the result.
//  6. Do not modify the rows.
//
// Example (one column, sorting [3, NULL, 1, 2] with a stable sort):
//
//	Asc(0)                         → 1, 2, 3, NULL   (default NULLS LAST)
//	Desc(0)                        → NULL, 3, 2, 1   (default NULLS FIRST)
//	SortKey{0, false, true}        → NULL, 1, 2, 3   (ASC NULLS FIRST)
//	SortKey{0, true, false}        → 3, 2, 1, NULL   (DESC NULLS LAST)
//	CompareRows((1,'b'), (1,'a'), [Asc(0), Desc(1)]) > 0
//
// Constraints: O(len(keys)), no allocation.
// Postgres reference: ApplySortComparator() in src/include/utils/sortsupport.h
func CompareRows(a, b types.Row, keys []SortKey) int {
	for _, k := range keys {
		x, y := a[k.Col], b[k.Col]
		switch {
		case x.IsNull() && y.IsNull():
			continue
		case x.IsNull():
			if k.NullsFirst {
				return -1
			}
			return 1
		case y.IsNull():
			if k.NullsFirst {
				return 1
			}
			return -1
		}
		c := types.Compare(x, y)
		if k.Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	return 0
}
