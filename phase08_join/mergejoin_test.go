package join

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// sortedEmployees / sortedDepartments are the 8.2 data sorted on the join key, NULLS LAST.
func sortedEmployees() []types.Row   { return sortByCols(employees(), []int{2}) }
func sortedDepartments() []types.Row { return sortByCols(departments(), []int{0}) }

func TestP8_10_MergeJoinInner_SortedInputs(t *testing.T) {
	// Guard: the test data really is in the order the problem statement shows.
	testkit.AssertRows(t, sortedEmployees(), testkit.Rows(
		[]any{1, "alice", 10}, []any{3, "carol", 10}, []any{2, "bob", 20}, []any{7, "grace", 20},
		[]any{6, "frank", 30}, []any{5, "erin", 40}, []any{4, "dave", nil},
	))
	testkit.AssertRows(t, sortedDepartments(), departments())
}

func TestP8_10_MergeJoinInner(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
	}{
		{"duplicate runs on both sides", empDept(Inner), sortedEmployees(), sortedDepartments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{2, "bob", 20, 20, "sales-emea"},
			[]any{7, "grace", 20, 20, "sales"},
			[]any{7, "grace", 20, 20, "sales-emea"},
			[]any{6, "frank", 30, 30, "ops"},
		)},
		{"extra inside a group", withExtra(empDept(Inner), notEmea), sortedEmployees(), sortedDepartments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{7, "grace", 20, 20, "sales"},
			[]any{6, "frank", 30, 30, "ops"},
		)},
		{"NULL keys on both sides never match", empDept(Inner),
			testkit.Rows([]any{4, "dave", nil}, []any{8, "hank", nil}),
			testkit.Rows([]any{nil, "unassigned"}, []any{nil, "limbo"}), nil},
		{"empty left", empDept(Inner), nil, sortedDepartments(), nil},
		{"empty right", empDept(Inner), sortedEmployees(), nil, nil},
		{"3×3 group is a 9-row cross product", JoinSpec{Type: Inner, Keys: []KeyPair{{0, 0}}, LeftWidth: 2, RightWidth: 2},
			testkit.Rows([]any{1, "a"}, []any{5, "l1"}, []any{5, "l2"}, []any{5, "l3"}, []any{6, "z"}),
			testkit.Rows([]any{5, "r1"}, []any{5, "r2"}, []any{5, "r3"}, []any{6, "y"}),
			testkit.Rows(
				[]any{5, "l1", 5, "r1"}, []any{5, "l1", 5, "r2"}, []any{5, "l1", 5, "r3"},
				[]any{5, "l2", 5, "r1"}, []any{5, "l2", 5, "r2"}, []any{5, "l2", 5, "r3"},
				[]any{5, "l3", 5, "r1"}, []any{5, "l3", 5, "r2"}, []any{5, "l3", 5, "r3"},
				[]any{6, "z", 6, "y"},
			)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MergeJoinInner(tc.left, tc.right, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
		})
	}
}

// With two keys, (1, NULL) sorts between (1, 5) and (2, 0): it must not
// match the right row (1, NULL) and must not stop the merge.
func TestP8_10_MergeJoinInner_MultiKeyNullInTheMiddle(t *testing.T) {
	spec := JoinSpec{Type: Inner, Keys: []KeyPair{{0, 0}, {1, 1}}, LeftWidth: 3, RightWidth: 3}
	left := testkit.Rows([]any{1, 5, "a"}, []any{1, nil, "b"}, []any{2, 0, "c"})
	right := testkit.Rows([]any{1, 5, "x"}, []any{1, nil, "y"}, []any{2, 0, "z"})
	got, err := MergeJoinInner(left, right, spec)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, got, testkit.Rows([]any{1, 5, "a", 1, 5, "x"}, []any{2, 0, "c", 2, 0, "z"}))
}

func TestP8_10_MergeJoinInner_Errors(t *testing.T) {
	for _, jt := range []JoinType{Left, Right, Full, Semi, Anti} {
		if _, err := MergeJoinInner(sortedEmployees(), sortedDepartments(), empDept(jt)); !errors.Is(err, ErrUnsupportedJoin) {
			t.Fatalf("%v: err = %v, want ErrUnsupportedJoin", jt, err)
		}
	}
	rows, err := MergeJoinInner(sortedEmployees(), sortedDepartments(), withExtra(empDept(Inner), failOnBob))
	if !errors.Is(err, errBoom) || rows != nil {
		t.Fatalf("rows=%v err=%v, want nil, errBoom", rows, err)
	}
}

func TestP8_10_MergeJoinInner_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	sl, sr := sortByCols(left, []int{1}), sortByCols(right, []int{1})
	for _, extra := range []bool{false, true} {
		spec := randSpec(Inner, extra)
		got, err := MergeJoinInner(sl, sr, spec)
		if err != nil {
			t.Fatal(err)
		}
		// On sorted inputs the merge order equals the nested-loop order.
		testkit.AssertRows(t, got, oracleJoin(spec, sl, sr))
		testkit.AssertRowsUnordered(t, got, randOracle(Inner, extra))
	}
}

// ---------------------------------------------------------------- 8.11

func TestP8_11_MergeJoinOuter(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
	}{
		{"LEFT", empDept(Left), sortedEmployees(), sortedDepartments(), leftRowsWant()},
		{"FULL", empDept(Full), sortedEmployees(), sortedDepartments(), fullRowsWant()},
		{"FULL with extra e.id <> 6", withExtra(empDept(Full), notFrank), sortedEmployees(), sortedDepartments(), fullNotFrankWant()},
		{"LEFT with extra rejecting one group partner", withExtra(empDept(Left), notEmea), sortedEmployees(), sortedDepartments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{4, "dave", nil, nil, nil},
			[]any{5, "erin", 40, nil, nil},
			[]any{6, "frank", 30, 30, "ops"},
			[]any{7, "grace", 20, 20, "sales"},
		)},
		{"FULL where extra rejects a whole group", withExtra(empDept(Full), notEmea),
			testkit.Rows([]any{2, "bob", 20}),
			testkit.Rows([]any{20, "sales-emea"}, []any{20, "sales-emea"}),
			testkit.Rows([]any{2, "bob", 20, nil, nil}, []any{nil, nil, nil, 20, "sales-emea"}, []any{nil, nil, nil, 20, "sales-emea"})},
		{"FULL, empty left", empDept(Full), nil, sortedDepartments()[4:], unmatchedDeptsWant()},
		{"LEFT, empty right", empDept(Left), sortedEmployees()[5:], nil, testkit.Rows(
			[]any{5, "erin", 40, nil, nil}, []any{4, "dave", nil, nil, nil},
		)},
		{"FULL, NULL keys on both sides", empDept(Full),
			testkit.Rows([]any{4, "dave", nil}),
			testkit.Rows([]any{nil, "unassigned"}),
			testkit.Rows([]any{4, "dave", nil, nil, nil}, []any{nil, nil, nil, nil, "unassigned"})},
		{"FULL, both empty", empDept(Full), nil, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MergeJoinOuter(tc.left, tc.right, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRowsUnordered(t, got, tc.want)
		})
	}
}

func TestP8_11_MergeJoinOuter_Errors(t *testing.T) {
	for _, jt := range []JoinType{Inner, Right, Semi, Anti} {
		if _, err := MergeJoinOuter(sortedEmployees(), sortedDepartments(), empDept(jt)); !errors.Is(err, ErrUnsupportedJoin) {
			t.Fatalf("%v: err = %v, want ErrUnsupportedJoin", jt, err)
		}
	}
	rows, err := MergeJoinOuter(sortedEmployees(), sortedDepartments(), withExtra(empDept(Full), failOnBob))
	if !errors.Is(err, errBoom) || rows != nil {
		t.Fatalf("rows=%v err=%v, want nil, errBoom", rows, err)
	}
}

func TestP8_11_MergeJoinOuter_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	sl, sr := sortByCols(left, []int{1}), sortByCols(right, []int{1})
	for _, jt := range []JoinType{Left, Full} {
		for _, extra := range []bool{false, true} {
			got, err := MergeJoinOuter(sl, sr, randSpec(jt, extra))
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRowsUnordered(t, got, randOracle(jt, extra))
		}
	}
}
