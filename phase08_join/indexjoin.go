package join

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 8.12 — IndexNestedLoopJoin [Medium]
//
// Join each outer row with the inner rows found by ONE index lookup on the join key, instead of rescanning the inner.
//
// A nested loop whose inner side is an index scan parameterized by the outer
// row ("Index Cond: (d.id = e.dept_id)") costs |outer| index lookups instead
// of |outer| full scans. It is the plan of choice when the outer side is
// small and the inner side is big and indexed.
//
// Rules:
//  1. spec.Type must be Inner or Left, otherwise (nil, ErrUnsupportedJoin)
//     without opening anything.
//  2. idx indexes the right rows on spec.RightCols() (idx.Cols equals it).
//     Open outer once, read it to the end, close it.
//  3. For each outer row: if any of its key values (spec.LeftCols()) is
//     NULL, do NOT call the index (it cannot match). Otherwise call
//     idx.Lookup exactly once with the outer key values, in Keys order.
//  4. Each returned row is a candidate: confirm with spec.Match (applies
//     Extra) and emit Concat(outer, inner) in the order Lookup returned them.
//  5. Left: an outer row with no confirmed match →
//     Concat(outer, NullRow(spec.RightWidth)) at its position.
//  6. Output order: outer order, then lookup order.
//  7. Close the outer exactly once, also on error; on error return (nil, err).
//
// Example (data of 8.2, index on departments.id):
//
//	INNER → the 7 rows of 8.2, idx.Lookups = 6 (dave's NULL key needs no lookup)
//	LEFT  → the 9 rows of 8.3, idx.Lookups = 6
//
// Constraints: exactly one Lookup per outer row with a non-NULL key; never scan the inner.
// Postgres reference: ExecNestLoop() with nestParams + ExecReScanIndexScan() in src/backend/executor/nodeIndexscan.c
func IndexNestedLoopJoin(outer exec.Iterator, idx *FakeIndex, spec JoinSpec) ([]types.Row, error) {
	if spec.Type != Inner && spec.Type != Left {
		return nil, ErrUnsupportedJoin
	}
	if err := outer.Open(); err != nil {
		return nil, err
	}
	var out []types.Row
	for {
		o, ok, err := outer.Next()
		if err != nil {
			outer.Close()
			return nil, err
		}
		if !ok {
			break
		}
		found := false
		key := make([]types.Value, len(spec.Keys))
		hasNull := false
		for i, k := range spec.Keys {
			key[i] = o[k.Left]
			hasNull = hasNull || key[i].IsNull()
		}
		if !hasNull {
			for _, in := range idx.Lookup(key) {
				m, err := spec.Match(o, in)
				if err != nil {
					outer.Close()
					return nil, err
				}
				if m {
					found = true
					out = append(out, Concat(o, in))
				}
			}
		}
		if !found && spec.Type == Left {
			out = append(out, Concat(o, NullRow(spec.RightWidth)))
		}
	}
	if err := outer.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
