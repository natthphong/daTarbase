package join

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// ---------------------------------------------------------------- shared dataset
//
// employees (id, name, dept_id) ⨝ departments (id, name) ON e.dept_id = d.id
//
//   - dept 50 'legal' has no employees
//   - dave has a NULL dept, and department 'unassigned' has a NULL id
//   - erin's dept 40 does not exist
//   - key 20 is duplicated on both sides (bob, grace × sales, sales-emea)
//   - key 10 is duplicated on the left (alice, carol)

func employees() []types.Row {
	return testkit.Rows(
		[]any{1, "alice", 10},
		[]any{2, "bob", 20},
		[]any{3, "carol", 10},
		[]any{4, "dave", nil},
		[]any{5, "erin", 40},
		[]any{6, "frank", 30},
		[]any{7, "grace", 20},
	)
}

func departments() []types.Row {
	return testkit.Rows(
		[]any{10, "eng"},
		[]any{20, "sales"},
		[]any{20, "sales-emea"},
		[]any{30, "ops"},
		[]any{50, "legal"},
		[]any{nil, "unassigned"},
	)
}

// empDept is the ON e.dept_id = d.id spec.
func empDept(jt JoinType) JoinSpec {
	return JoinSpec{Type: jt, Keys: []KeyPair{{Left: 2, Right: 0}}, LeftWidth: 3, RightWidth: 2}
}

// withExtra returns spec with an Extra predicate.
func withExtra(s JoinSpec, f func(types.Row) (bool, error)) JoinSpec {
	s.Extra = f
	return s
}

// notEmea is the Extra "d.name <> 'sales-emea'" on the combined row.
func notEmea(r types.Row) (bool, error) { return r[4].S != "sales-emea", nil }

// notFrank is the Extra "e.id <> 6" on the combined row.
func notFrank(r types.Row) (bool, error) { return r[0].I != 6, nil }

var errBoom = errors.New("boom")

// failOnBob is an Extra that fails for employee 2.
func failOnBob(r types.Row) (bool, error) {
	if r[0].I == 2 {
		return false, errBoom
	}
	return true, nil
}

func innerRowsWant() []types.Row {
	return testkit.Rows(
		[]any{1, "alice", 10, 10, "eng"},
		[]any{2, "bob", 20, 20, "sales"},
		[]any{2, "bob", 20, 20, "sales-emea"},
		[]any{3, "carol", 10, 10, "eng"},
		[]any{6, "frank", 30, 30, "ops"},
		[]any{7, "grace", 20, 20, "sales"},
		[]any{7, "grace", 20, 20, "sales-emea"},
	)
}

func leftRowsWant() []types.Row {
	return testkit.Rows(
		[]any{1, "alice", 10, 10, "eng"},
		[]any{2, "bob", 20, 20, "sales"},
		[]any{2, "bob", 20, 20, "sales-emea"},
		[]any{3, "carol", 10, 10, "eng"},
		[]any{4, "dave", nil, nil, nil},
		[]any{5, "erin", 40, nil, nil},
		[]any{6, "frank", 30, 30, "ops"},
		[]any{7, "grace", 20, 20, "sales"},
		[]any{7, "grace", 20, 20, "sales-emea"},
	)
}

func unmatchedDeptsWant() []types.Row {
	return testkit.Rows(
		[]any{nil, nil, nil, 50, "legal"},
		[]any{nil, nil, nil, nil, "unassigned"},
	)
}

func rightRowsWant() []types.Row { return append(innerRowsWant(), unmatchedDeptsWant()...) }
func fullRowsWant() []types.Row  { return append(leftRowsWant(), unmatchedDeptsWant()...) }

// fullNotFrankWant is FULL JOIN ... ON e.dept_id = d.id AND e.id <> 6, in nested-loop / hash order.
func fullNotFrankWant() []types.Row {
	return testkit.Rows(
		[]any{1, "alice", 10, 10, "eng"},
		[]any{2, "bob", 20, 20, "sales"},
		[]any{2, "bob", 20, 20, "sales-emea"},
		[]any{3, "carol", 10, 10, "eng"},
		[]any{4, "dave", nil, nil, nil},
		[]any{5, "erin", 40, nil, nil},
		[]any{6, "frank", 30, nil, nil},
		[]any{7, "grace", 20, 20, "sales"},
		[]any{7, "grace", 20, 20, "sales-emea"},
		[]any{nil, nil, nil, 30, "ops"},
		[]any{nil, nil, nil, 50, "legal"},
		[]any{nil, nil, nil, nil, "unassigned"},
	)
}

// ---------------------------------------------------------------- iterators

// failIter returns its rows, then errBoom instead of end-of-input.
type failIter struct {
	rows          []types.Row
	pos           int
	Opens, Closes int
}

func (f *failIter) Open() error { f.pos = 0; f.Opens++; return nil }
func (f *failIter) Next() (types.Row, bool, error) {
	if f.pos >= len(f.rows) {
		return nil, false, errBoom
	}
	f.pos++
	return f.rows[f.pos-1], true, nil
}
func (f *failIter) Close() error { f.Closes++; return nil }

var _ exec.Iterator = (*failIter)(nil)

// checkBalanced fails unless every Open was matched by a Close.
func checkBalanced(t *testing.T, name string, opens, closes int) {
	t.Helper()
	if opens != closes {
		t.Fatalf("%s: Opens = %d, Closes = %d, want equal", name, opens, closes)
	}
}

// ---------------------------------------------------------------- oracle
//
// oracleJoin is an independent, obviously-correct nested loop over slices.
// Output order = nested-loop order: left order, right order; for RIGHT/FULL
// the unmatched right rows come last, in right order.

func oracleMatch(s JoinSpec, l, r types.Row) bool {
	for _, k := range s.Keys {
		a, b := l[k.Left], r[k.Right]
		if a.IsNull() || b.IsNull() || types.Compare(a, b) != 0 {
			return false
		}
	}
	if s.Extra == nil {
		return true
	}
	ok, err := s.Extra(Concat(l, r))
	if err != nil {
		panic(err)
	}
	return ok
}

func oracleJoin(s JoinSpec, left, right []types.Row) []types.Row {
	var out []types.Row
	rMatched := make([]bool, len(right))
	for _, l := range left {
		found := false
		for j, r := range right {
			if !oracleMatch(s, l, r) {
				continue
			}
			found, rMatched[j] = true, true
			if s.Type != Semi && s.Type != Anti {
				out = append(out, Concat(l, r))
			}
		}
		switch {
		case s.Type == Semi && found, s.Type == Anti && !found:
			out = append(out, l)
		case (s.Type == Left || s.Type == Full) && !found:
			out = append(out, Concat(l, NullRow(s.RightWidth)))
		}
	}
	if s.Type == Right || s.Type == Full {
		for j, r := range right {
			if !rMatched[j] {
				out = append(out, Concat(NullRow(s.LeftWidth), r))
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- random data

// randData is a 1000×1000 pair of inputs: left (id, k), right (id, k), keys
// in 0..199 with ~5% NULLs on each side, so there are duplicates on both sides.
func randData(seed uint64) (left, right []types.Row) {
	rng := rand.New(rand.NewPCG(seed, 99))
	gen := func(n int, idBase int) []types.Row {
		rows := make([]types.Row, n)
		for i := range rows {
			k := types.Int64(int64(rng.IntN(200)))
			if rng.IntN(20) == 0 {
				k = types.Null()
			}
			rows[i] = types.Row{types.Int64(int64(idBase + i)), k}
		}
		return rows
	}
	return gen(1000, 0), gen(1000, 100000)
}

func randSpec(jt JoinType, extra bool) JoinSpec {
	s := JoinSpec{Type: jt, Keys: []KeyPair{{Left: 1, Right: 1}}, LeftWidth: 2, RightWidth: 2}
	if extra {
		s.Extra = func(r types.Row) (bool, error) { return (r[0].I+r[2].I)%3 != 0, nil }
	}
	return s
}

type oracleKey struct {
	jt    JoinType
	extra bool
}

var oracleCache = map[oracleKey][]types.Row{}

// randOracle returns the oracle result for randData(1) under randSpec(jt, extra).
func randOracle(jt JoinType, extra bool) []types.Row {
	k := oracleKey{jt, extra}
	if r, ok := oracleCache[k]; ok {
		return r
	}
	l, r := randData(1)
	res := oracleJoin(randSpec(jt, extra), l, r)
	oracleCache[k] = res
	return res
}

// sortByCols returns a stable-sorted copy of rows in CompareKeys order on cols.
func sortByCols(rows []types.Row, cols []int) []types.Row {
	out := slices.Clone(rows)
	slices.SortStableFunc(out, func(a, b types.Row) int { return CompareKeys(a, cols, b, cols) })
	return out
}
