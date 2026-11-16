package join

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

type iterJoin func(outer, inner exec.Iterator, spec JoinSpec) ([]types.Row, error)

// runJoin runs fn over ValuesIters of left/right and returns the result plus the iterators.
func runJoin(t *testing.T, fn iterJoin, spec JoinSpec, left, right []types.Row) ([]types.Row, *exec.ValuesIter, *exec.ValuesIter) {
	t.Helper()
	l, r := exec.Values(left...), exec.Values(right...)
	got, err := fn(l, r, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checkBalanced(t, "left", l.Opens, l.Closes)
	checkBalanced(t, "right", r.Opens, r.Closes)
	return got, l, r
}

// checkUnsupported asserts that fn rejects jt without opening anything.
func checkUnsupported(t *testing.T, fn iterJoin, jts ...JoinType) {
	t.Helper()
	for _, jt := range jts {
		l, r := exec.Values(employees()...), exec.Values(departments()...)
		if _, err := fn(l, r, empDept(jt)); !errors.Is(err, ErrUnsupportedJoin) {
			t.Fatalf("%v: err = %v, want ErrUnsupportedJoin", jt, err)
		}
		if l.Opens+r.Opens != 0 {
			t.Fatalf("%v: opened inputs (%d, %d) before rejecting the join type", jt, l.Opens, r.Opens)
		}
	}
}

// checkErrors asserts that errors from the outer input, the inner input and
// Extra are returned and every opened iterator is closed.
func checkErrors(t *testing.T, fn iterJoin, jt JoinType) {
	t.Helper()
	type errCase struct {
		name         string
		outer, inner exec.Iterator
		spec         JoinSpec
		counts       func() (oOpens, oCloses, iOpens, iCloses int)
	}
	o1, i1 := &failIter{rows: employees()[:2]}, exec.Values(departments()...)
	o2, i2 := exec.Values(employees()...), &failIter{rows: departments()[:3]}
	o3, i3 := exec.Values(employees()...), exec.Values(departments()...)
	cases := []errCase{
		{"outer fails", o1, i1, empDept(jt), func() (int, int, int, int) { return o1.Opens, o1.Closes, i1.Opens, i1.Closes }},
		{"inner fails", o2, i2, empDept(jt), func() (int, int, int, int) { return o2.Opens, o2.Closes, i2.Opens, i2.Closes }},
		{"extra fails", o3, i3, withExtra(empDept(jt), failOnBob), func() (int, int, int, int) { return o3.Opens, o3.Closes, i3.Opens, i3.Closes }},
	}
	for _, tc := range cases {
		t.Run(jt.String()+"/"+tc.name, func(t *testing.T) {
			got, err := fn(tc.outer, tc.inner, tc.spec)
			if !errors.Is(err, errBoom) {
				t.Fatalf("err = %v, want errBoom", err)
			}
			if got != nil {
				t.Fatalf("rows = %v, want nil on error", got)
			}
			oo, oc, io, ic := tc.counts()
			checkBalanced(t, "outer", oo, oc)
			checkBalanced(t, "inner", io, ic)
		})
	}
}

// ---------------------------------------------------------------- 8.2

func TestP8_2_NestedLoopInner(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
		innerOpens  int
	}{
		{"employees ⨝ departments", empDept(Inner), employees(), departments(), innerRowsWant(), 7},
		{"extra d.name <> 'sales-emea'", withExtra(empDept(Inner), notEmea), employees(), departments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{6, "frank", 30, 30, "ops"},
			[]any{7, "grace", 20, 20, "sales"},
		), 7},
		{"empty outer never opens inner", empDept(Inner), nil, departments(), nil, 0},
		{"empty inner", empDept(Inner), employees(), nil, nil, 7},
		{"no keys is a cross join", JoinSpec{Type: Inner, LeftWidth: 1, RightWidth: 1},
			testkit.Rows([]any{1}, []any{2}), testkit.Rows([]any{"a"}, []any{"b"}),
			testkit.Rows([]any{1, "a"}, []any{1, "b"}, []any{2, "a"}, []any{2, "b"}), 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, l, r := runJoin(t, NestedLoopInner, tc.spec, tc.left, tc.right)
			testkit.AssertRows(t, got, tc.want)
			if l.Opens != 1 {
				t.Fatalf("outer opened %d times, want 1", l.Opens)
			}
			if r.Opens != tc.innerOpens {
				t.Fatalf("inner opened %d times, want %d (one rescan per outer row)", r.Opens, tc.innerOpens)
			}
		})
	}
}

func TestP8_2_NestedLoopInner_RescanCounts(t *testing.T) {
	_, l, r := runJoin(t, NestedLoopInner, empDept(Inner), employees(), departments())
	if l.Pulled != 7 || r.Pulled != 42 {
		t.Fatalf("pulled outer=%d inner=%d, want 7 and 42 (7 full inner scans of 6 rows)", l.Pulled, r.Pulled)
	}
}

func TestP8_2_NestedLoopInner_Errors(t *testing.T) {
	checkUnsupported(t, NestedLoopInner, Left, Right, Full, Semi, Anti)
	checkErrors(t, NestedLoopInner, Inner)
}

func TestP8_2_NestedLoopInner_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	for _, extra := range []bool{false, true} {
		got, _, r := runJoin(t, NestedLoopInner, randSpec(Inner, extra), left, right)
		testkit.AssertRows(t, got, randOracle(Inner, extra))
		if r.Opens != 1000 {
			t.Fatalf("inner opened %d times, want 1000", r.Opens)
		}
	}
}

// ---------------------------------------------------------------- 8.3

func TestP8_3_NestedLoopLeft(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
	}{
		{"employees ⟕ departments", empDept(Left), employees(), departments(), leftRowsWant()},
		{"extra keeps one match per sales employee", withExtra(empDept(Left), notEmea), employees(), departments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{4, "dave", nil, nil, nil},
			[]any{5, "erin", 40, nil, nil},
			[]any{6, "frank", 30, 30, "ops"},
			[]any{7, "grace", 20, 20, "sales"},
		)},
		{"extra rejects frank's only match", withExtra(empDept(Left), notFrank), employees(), departments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{2, "bob", 20, 20, "sales-emea"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{4, "dave", nil, nil, nil},
			[]any{5, "erin", 40, nil, nil},
			[]any{6, "frank", 30, nil, nil},
			[]any{7, "grace", 20, 20, "sales"},
			[]any{7, "grace", 20, 20, "sales-emea"},
		)},
		{"empty inner pads every row", empDept(Left), employees()[:2], nil, testkit.Rows(
			[]any{1, "alice", 10, nil, nil},
			[]any{2, "bob", 20, nil, nil},
		)},
		{"empty outer", empDept(Left), nil, departments(), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, l, r := runJoin(t, NestedLoopLeft, tc.spec, tc.left, tc.right)
			testkit.AssertRows(t, got, tc.want)
			if l.Opens != 1 || r.Opens != len(tc.left) {
				t.Fatalf("opens outer=%d inner=%d, want 1 and %d", l.Opens, r.Opens, len(tc.left))
			}
		})
	}
}

func TestP8_3_NestedLoopLeft_Errors(t *testing.T) {
	checkUnsupported(t, NestedLoopLeft, Inner, Right, Full, Semi, Anti)
	checkErrors(t, NestedLoopLeft, Left)
}

func TestP8_3_NestedLoopLeft_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	for _, extra := range []bool{false, true} {
		got, _, _ := runJoin(t, NestedLoopLeft, randSpec(Left, extra), left, right)
		testkit.AssertRows(t, got, randOracle(Left, extra))
	}
}

// ---------------------------------------------------------------- 8.4

func TestP8_4_NestedLoopRightFull(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
		innerOpens  int
	}{
		{"RIGHT", empDept(Right), employees(), departments(), rightRowsWant(), 7},
		{"FULL", empDept(Full), employees(), departments(), fullRowsWant(), 7},
		{"FULL with extra e.id <> 6", withExtra(empDept(Full), notFrank), employees(), departments(), fullNotFrankWant(), 7},
		{"RIGHT with extra e.id <> 6", withExtra(empDept(Right), notFrank), employees(), departments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{2, "bob", 20, 20, "sales-emea"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{7, "grace", 20, 20, "sales"},
			[]any{7, "grace", 20, 20, "sales-emea"},
			[]any{nil, nil, nil, 30, "ops"},
			[]any{nil, nil, nil, 50, "legal"},
			[]any{nil, nil, nil, nil, "unassigned"},
		), 7},
		{"RIGHT with empty outer scans inner once", empDept(Right), nil, departments()[:2], testkit.Rows(
			[]any{nil, nil, nil, 10, "eng"},
			[]any{nil, nil, nil, 20, "sales"},
		), 1},
		{"FULL with empty outer", empDept(Full), nil, departments()[4:], unmatchedDeptsWant(), 1},
		{"FULL with empty inner", empDept(Full), employees()[3:5], nil, testkit.Rows(
			[]any{4, "dave", nil, nil, nil},
			[]any{5, "erin", 40, nil, nil},
		), 2},
		{"RIGHT with empty inner", empDept(Right), employees(), nil, nil, 7},
		{"both empty", empDept(Full), nil, nil, nil, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, l, r := runJoin(t, NestedLoopRightFull, tc.spec, tc.left, tc.right)
			testkit.AssertRows(t, got, tc.want)
			if l.Opens != 1 || r.Opens != tc.innerOpens {
				t.Fatalf("opens outer=%d inner=%d, want 1 and %d", l.Opens, r.Opens, tc.innerOpens)
			}
		})
	}
}

func TestP8_4_NestedLoopRightFull_Errors(t *testing.T) {
	checkUnsupported(t, NestedLoopRightFull, Inner, Left, Semi, Anti)
	checkErrors(t, NestedLoopRightFull, Right)
	checkErrors(t, NestedLoopRightFull, Full)
}

func TestP8_4_NestedLoopRightFull_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	for _, jt := range []JoinType{Right, Full} {
		for _, extra := range []bool{false, true} {
			got, _, _ := runJoin(t, NestedLoopRightFull, randSpec(jt, extra), left, right)
			testkit.AssertRows(t, got, randOracle(jt, extra))
		}
	}
}

// ---------------------------------------------------------------- 8.5

func TestP8_5_NestedLoopSemiAnti(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
		innerPulled int
	}{
		{"SEMI = EXISTS", empDept(Semi), employees(), departments(), testkit.Rows(
			[]any{1, "alice", 10}, []any{2, "bob", 20}, []any{3, "carol", 10}, []any{6, "frank", 30}, []any{7, "grace", 20},
		), 22},
		{"ANTI = NOT EXISTS keeps the NULL key", empDept(Anti), employees(), departments(), testkit.Rows(
			[]any{4, "dave", nil}, []any{5, "erin", 40},
		), 22},
		{"SEMI emits bob once despite two matches", empDept(Semi), employees()[1:2], departments(), testkit.Rows(
			[]any{2, "bob", 20},
		), 2},
		{"SEMI with extra skips the first candidate", withExtra(empDept(Semi), func(r types.Row) (bool, error) {
			return r[4].S == "sales-emea", nil
		}), employees()[1:2], departments(), testkit.Rows([]any{2, "bob", 20}), 3},
		{"ANTI with empty inner returns everything", empDept(Anti), employees()[:3], nil, employees()[:3], 0},
		{"SEMI with empty inner", empDept(Semi), employees(), nil, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, l, r := runJoin(t, NestedLoopSemiAnti, tc.spec, tc.left, tc.right)
			testkit.AssertRows(t, got, tc.want)
			if l.Opens != 1 || r.Opens != len(tc.left) {
				t.Fatalf("opens outer=%d inner=%d, want 1 and %d", l.Opens, r.Opens, len(tc.left))
			}
			if r.Pulled != tc.innerPulled {
				t.Fatalf("inner rows pulled = %d, want %d (stop at the first match)", r.Pulled, tc.innerPulled)
			}
		})
	}
}

func TestP8_5_NestedLoopSemiAnti_Errors(t *testing.T) {
	checkUnsupported(t, NestedLoopSemiAnti, Inner, Left, Right, Full)
	checkErrors(t, NestedLoopSemiAnti, Semi)
	checkErrors(t, NestedLoopSemiAnti, Anti)
}

func TestP8_5_NestedLoopSemiAnti_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	for _, jt := range []JoinType{Semi, Anti} {
		for _, extra := range []bool{false, true} {
			got, _, _ := runJoin(t, NestedLoopSemiAnti, randSpec(jt, extra), left, right)
			testkit.AssertRows(t, got, randOracle(jt, extra))
		}
	}
}

// ---------------------------------------------------------------- 8.6

func TestP8_6_NotIn(t *testing.T) {
	deptsNoNull := departments()[:5]
	tests := []struct {
		name         string
		outer, inner []types.Row
		want         []types.Row
	}{
		{"inner has a NULL → no rows (the trap)", employees(), departments(), nil},
		{"no NULL in inner: dave's NULL is still excluded", employees(), deptsNoNull, testkit.Rows([]any{5, "erin", 40})},
		{"empty inner keeps everything, even NULL", employees(), nil, employees()},
		{"inner only NULL", employees()[:2], testkit.Rows([]any{nil, "x"}), nil},
		{"duplicate values in inner", testkit.Rows([]any{1, "a", 1}, []any{2, "b", 2}, []any{3, "c", 3}),
			testkit.Rows([]any{2, "x"}, []any{2, "y"}, []any{2, "z"}),
			testkit.Rows([]any{1, "a", 1}, []any{3, "c", 3})},
		{"float 20.0 equals int 20", testkit.Rows([]any{2, "bob", 20}, []any{1, "alice", 10}),
			testkit.Rows([]any{20.0, "x"}), testkit.Rows([]any{1, "alice", 10})},
		{"empty outer", nil, deptsNoNull, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, i := exec.Values(tc.outer...), exec.Values(tc.inner...)
			got, err := NotIn(o, i, 2, 0)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			testkit.AssertRows(t, got, tc.want)
			if i.Opens != 1 {
				t.Fatalf("inner opened %d times, want 1 (read once into a hash set)", i.Opens)
			}
			checkBalanced(t, "outer", o.Opens, o.Closes)
			checkBalanced(t, "inner", i.Opens, i.Closes)
		})
	}
}

// NOT EXISTS (8.5) and NOT IN differ exactly on NULLs.
func TestP8_6_NotIn_DiffersFromAnti(t *testing.T) {
	deptsNoNull := departments()[:5]
	anti, _, _ := runJoin(t, NestedLoopSemiAnti, empDept(Anti), employees(), deptsNoNull)
	notIn, err := NotIn(exec.Values(employees()...), exec.Values(deptsNoNull...), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, anti, testkit.Rows([]any{4, "dave", nil}, []any{5, "erin", 40}))
	testkit.AssertRows(t, notIn, testkit.Rows([]any{5, "erin", 40}))
}

func TestP8_6_NotIn_Errors(t *testing.T) {
	o, i := exec.Values(employees()...), &failIter{rows: departments()[:2]}
	if _, err := NotIn(o, i, 2, 0); !errors.Is(err, errBoom) {
		t.Fatalf("inner error: err = %v, want errBoom", err)
	}
	checkBalanced(t, "inner", i.Opens, i.Closes)
	checkBalanced(t, "outer", o.Opens, o.Closes)

	o2, i2 := &failIter{rows: employees()[:1]}, exec.Values(departments()[:2]...)
	if _, err := NotIn(o2, i2, 2, 0); !errors.Is(err, errBoom) {
		t.Fatalf("outer error: err = %v, want errBoom", err)
	}
	checkBalanced(t, "inner", i2.Opens, i2.Closes)
	checkBalanced(t, "outer", o2.Opens, o2.Closes)
}
