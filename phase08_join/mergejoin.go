package join

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// The merge join problems receive both inputs as slices that are ALREADY
// SORTED on their key columns (left on spec.LeftCols(), right on
// spec.RightCols()) in CompareKeys order: ascending, NULLS LAST. Think of
// them as the output of two Sort nodes or two index scans.
// You must not sort them again or build a hash table.

// Problem 8.10 — MergeJoinInner [Hard]
//
// Compute an INNER equi-join of two sorted inputs in one merge pass, re-reading duplicate runs with mark/restore.
//
// Walk both inputs with a cursor each. Compare the current keys
// (CompareKeys): advance the side with the smaller key. When they are
// equal you have found a key group. Every left row of the group must be
// paired with every right row of the group, so before walking the right group
// MARK its first position; after finishing one left row, RESTORE the right
// cursor to the mark and replay the group for the next left row if it has the
// same key.
//
//	left:  10 10 20 20 30 40 NULL          right: 10 20 20 30 50 NULL
//	       ^^^^^ group 10  × eng                  ^ mark
//	             ^^^^^ group 20 × (sales, sales-emea)   ← restore to mark for grace
//
// Rules:
//  1. spec.Type must be Inner, otherwise (nil, ErrUnsupportedJoin).
//  2. Both inputs are sorted as described above. Rows whose key contains a
//     NULL never match (they sort to the end, but with multiple keys a row
//     like (1, NULL) can sit between (1, 5) and (2, 0) — CompareKeys says 0
//     when both sides have NULL in the same place; that is NOT a match).
//  3. For each pair with equal non-NULL keys, confirm with spec.Match (it
//     applies Extra) and emit Concat(left, right).
//  4. Output order: left input order; for one left row, right input order.
//     (With sorted inputs this is the same order the nested loop would give.)
//  5. Each left row is examined once. Right rows are re-read only inside a
//     duplicate group (restore to the mark), never from the start.
//  6. An error from spec.Match aborts: return (nil, err).
//
// Example (the data of 8.2, sorted):
//
//	left:  (1,'alice',10) (3,'carol',10) (2,'bob',20) (7,'grace',20) (6,'frank',30) (5,'erin',40) (4,'dave',NULL)
//	right: (10,'eng') (20,'sales') (20,'sales-emea') (30,'ops') (50,'legal') (NULL,'unassigned')
//	→ (1,'alice',10,10,'eng') (3,'carol',10,10,'eng')
//	  (2,'bob',20,20,'sales') (2,'bob',20,20,'sales-emea')
//	  (7,'grace',20,20,'sales') (7,'grace',20,20,'sales-emea')
//	  (6,'frank',30,30,'ops')
//
// Constraints: O(|left| + |right| + output) comparisons; O(1) extra memory.
// Postgres reference: ExecMergeJoin() (EXEC_MJ_JOINTUPLES, ExecMarkPos/ExecRestrPos) in src/backend/executor/nodeMergejoin.c
func MergeJoinInner(left, right []types.Row, spec JoinSpec) ([]types.Row, error) {
	if spec.Type != Inner {
		return nil, ErrUnsupportedJoin
	}
	lcols, rcols := spec.LeftCols(), spec.RightCols()
	var out []types.Row
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		c := CompareKeys(left[i], lcols, right[j], rcols)
		if c == 0 {
			if _, hasNull := KeyOf(left[i], lcols); hasNull {
				c = -1 // equal only because of NULLs: not a match
			}
		}
		switch {
		case c < 0:
			i++
		case c > 0:
			j++
		default:
			mark := j
			end := j
			for end < len(right) && CompareKeys(left[i], lcols, right[end], rcols) == 0 {
				end++
			}
			// Replay right[mark:end] for every left row of this key group.
			for i < len(left) && CompareKeys(left[i], lcols, right[mark], rcols) == 0 {
				for k := mark; k < end; k++ {
					m, err := spec.Match(left[i], right[k])
					if err != nil {
						return nil, err
					}
					if m {
						out = append(out, Concat(left[i], right[k]))
					}
				}
				i++
			}
			j = end
		}
	}
	return out, nil
}

// Problem 8.11 — MergeJoinOuter [Hard]
//
// Compute LEFT and FULL merge joins: emit unmatched rows as the merge passes them.
//
// In a merge, a row that is skipped because the other side's key is already
// larger can never match later — so it can be emitted (padded) right away.
// Inside an equal-key group a row is unmatched only if Extra rejected every
// partner, so track matches for the left row being processed and for every
// right row of the group.
//
// (A RIGHT merge join is the same as LEFT with the inputs swapped; Postgres
// does exactly that in the planner, so it is not part of this problem.)
//
// Rules:
//  1. spec.Type must be Left or Full, otherwise (nil, ErrUnsupportedJoin).
//  2. Inputs sorted as for 8.10. NULL-key rows never match.
//  3. Matching pairs (equal non-NULL keys and spec.Match true) →
//     Concat(left, right).
//  4. Every left row with no match → Concat(left, NullRow(spec.RightWidth)).
//  5. Full only: every right row with no match →
//     Concat(NullRow(spec.LeftWidth), right).
//  6. Output order is NOT checked (only the multiset), but do it in one
//     merge pass: no hash table, no nested loop over the whole right input.
//  7. An error from spec.Match aborts: return (nil, err).
//
// Example (the sorted data of 8.10):
//
//	LEFT → the 7 inner rows + (5,'erin',40,NULL,NULL) + (4,'dave',NULL,NULL,NULL)
//	FULL → those 9 + (NULL,NULL,NULL,50,'legal') + (NULL,NULL,NULL,NULL,'unassigned')
//	FULL with Extra "e.id <> 6" → also (6,'frank',30,NULL,NULL) and (NULL,NULL,NULL,30,'ops')
//	instead of (6,'frank',30,30,'ops'), 12 rows.
//
// Constraints: O(|left| + |right| + output) comparisons.
// Postgres reference: ExecMergeJoin() (EXEC_MJ_ENDOUTER / EXEC_MJ_ENDINNER, MJFillOuter/MJFillInner) in src/backend/executor/nodeMergejoin.c
func MergeJoinOuter(left, right []types.Row, spec JoinSpec) ([]types.Row, error) {
	if spec.Type != Left && spec.Type != Full {
		return nil, ErrUnsupportedJoin
	}
	full := spec.Type == Full
	lcols, rcols := spec.LeftCols(), spec.RightCols()
	var out []types.Row
	padLeft := func(l types.Row) { out = append(out, Concat(l, NullRow(spec.RightWidth))) }
	padRight := func(r types.Row) {
		if full {
			out = append(out, Concat(NullRow(spec.LeftWidth), r))
		}
	}
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		c := CompareKeys(left[i], lcols, right[j], rcols)
		if c == 0 {
			if _, hasNull := KeyOf(left[i], lcols); hasNull {
				c = -1
			}
		}
		switch {
		case c < 0:
			padLeft(left[i])
			i++
		case c > 0:
			padRight(right[j])
			j++
		default:
			mark, end := j, j
			for end < len(right) && CompareKeys(left[i], lcols, right[end], rcols) == 0 {
				end++
			}
			rMatched := make([]bool, end-mark)
			for i < len(left) && CompareKeys(left[i], lcols, right[mark], rcols) == 0 {
				found := false
				for k := mark; k < end; k++ {
					m, err := spec.Match(left[i], right[k])
					if err != nil {
						return nil, err
					}
					if m {
						found, rMatched[k-mark] = true, true
						out = append(out, Concat(left[i], right[k]))
					}
				}
				if !found {
					padLeft(left[i])
				}
				i++
			}
			for k := mark; k < end; k++ {
				if !rMatched[k-mark] {
					padRight(right[k])
				}
			}
			j = end
		}
	}
	for ; i < len(left); i++ {
		padLeft(left[i])
	}
	for ; j < len(right); j++ {
		padRight(right[j])
	}
	return out, nil
}
