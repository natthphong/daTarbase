package sortagg

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// WinKind is a window function.
type WinKind uint8

const (
	WinRowNumber WinKind = iota // ROW_NUMBER()
	WinRank                     // RANK()
	WinDenseRank                // DENSE_RANK()
	WinSum                      // SUM(col) with the default frame
)

// WinFunc is one window function call; Col is used only by WinSum.
type WinFunc struct {
	Kind WinKind
	Col  int
}

// WindowSpec is an OVER (PARTITION BY ... ORDER BY ...) clause.
type WindowSpec struct {
	PartitionBy []int     // partition columns (may be empty = one partition)
	OrderBy     []SortKey // ordering inside a partition (may be empty)
}

// Problem 7.15 — Window [Hard]
//
// Evaluate ROW_NUMBER, RANK, DENSE_RANK and running SUM over (PARTITION BY ... ORDER BY ...), appending one column per function.
//
// Unlike GROUP BY, a window function keeps every input row and adds a value
// computed from the row's partition. Postgres sorts by (partition, order)
// and walks each partition once, tracking "peer groups" — rows that tie on
// every ORDER BY key.
//
//	PARTITION BY dept ORDER BY salary DESC:
//	dept  name  salary | row_number rank dense_rank sum
//	eng   bob   120    |     1       1       1      120
//	eng   ann   100    |     2       2       2      320   ← ann and cat are peers:
//	eng   cat   100    |     3       2       2      320   ← same rank, same sum
//	eng   fay    90    |     4       4       3      410
//	ops   eve   NULL   |     1       1       1      NULL  (DESC → NULLS FIRST)
//	ops   dan    80    |     2       2       2      160
//	ops   gus    80    |     3       2       2      160
//
// Rules:
//  1. Open the child, read all rows, close it. Return the first error.
//  2. Output order: stable sort by every PartitionBy column with Asc (ASC
//     NULLS LAST), then by spec.OrderBy. Rows that tie on all of these keep input order.
//  3. A partition = consecutive sorted rows whose PartitionBy values are all
//     types.Identical. Peers = rows of the same partition with
//     CompareRows(·,·,spec.OrderBy) == 0. With no OrderBy, all rows of a partition are peers.
//  4. Each output row is a NEW row: the input columns followed by one value
//     per entry of funcs, in order. Never modify the input rows.
//  5. ROW_NUMBER: 1, 2, 3, ... within the partition (ties get different numbers).
//     RANK: 1 + number of partition rows before the row's first peer (gaps after ties).
//     DENSE_RANK: 1 + number of distinct peer groups before the row's (no gaps).
//     All three are int8.
//  6. SUM(col) uses Postgres' default frame RANGE BETWEEN UNBOUNDED PRECEDING
//     AND CURRENT ROW: it sums col over the partition's rows from its start
//     through the row's LAST PEER (so peers share one value). NULLs are
//     skipped. If that frame has no non-NULL value → NULL; else if any summed
//     value is float8 → Float64(sum), otherwise Int64(sum).
//  7. Empty input → no rows.
//
// Example: the table above. Without ORDER BY, every row of eng gets rank 1
// and sum 410, ROW_NUMBER follows input order. Without PARTITION BY and
// ORDER BY salary ASC: salaries 80,80,90,100,100,120,NULL get ranks
// 1,1,3,4,4,6,7, dense ranks 1,1,2,3,3,4,5 and sums 160,160,250,450,450,570,570.
//
// Constraints: O(n log n) for the sort, then one pass per partition.
// Postgres reference: ExecWindowAgg() in src/backend/executor/nodeWindowAgg.c,
// window_rank() / window_dense_rank() in src/backend/utils/adt/windowfuncs.c
func Window(child exec.Iterator, spec WindowSpec, funcs []WinFunc) (out []types.Row, err error) {
	panic("TODO: Problem 7.15")
}
