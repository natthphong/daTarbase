package join

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP8_12_IndexNestedLoopJoin(t *testing.T) {
	tests := []struct {
		name    string
		spec    JoinSpec
		outer   []types.Row
		want    []types.Row
		lookups int
	}{
		{"INNER", empDept(Inner), employees(), innerRowsWant(), 6},
		{"LEFT", empDept(Left), employees(), leftRowsWant(), 6},
		{"LEFT with extra e.id <> 6", withExtra(empDept(Left), notFrank), employees(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{2, "bob", 20, 20, "sales-emea"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{4, "dave", nil, nil, nil},
			[]any{5, "erin", 40, nil, nil},
			[]any{6, "frank", 30, nil, nil},
			[]any{7, "grace", 20, 20, "sales"},
			[]any{7, "grace", 20, 20, "sales-emea"},
		), 6},
		{"only NULL keys: no lookups", empDept(Left), employees()[3:4], testkit.Rows([]any{4, "dave", nil, nil, nil}), 0},
		{"empty outer", empDept(Inner), nil, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx := NewFakeIndex(departments(), []int{0})
			o := exec.Values(tc.outer...)
			got, err := IndexNestedLoopJoin(o, idx, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
			if idx.Lookups != tc.lookups {
				t.Fatalf("index lookups = %d, want %d (one per outer row with a non-NULL key)", idx.Lookups, tc.lookups)
			}
			if o.Opens != 1 || o.Closes != 1 || o.Pulled != len(tc.outer) {
				t.Fatalf("outer opens/closes/pulled = %d/%d/%d, want 1/1/%d", o.Opens, o.Closes, o.Pulled, len(tc.outer))
			}
		})
	}
}

func TestP8_12_IndexNestedLoopJoin_MultiKey(t *testing.T) {
	customers := testkit.Rows(
		[]any{"eu", 7, "anna"},
		[]any{"us", 7, "ben"},
		[]any{"eu", 7, "anna-2"},
	)
	// index on (cust, region) = right columns (1, 0)
	idx := NewFakeIndex(customers, []int{1, 0})
	spec := JoinSpec{Type: Inner, Keys: []KeyPair{{Left: 1, Right: 1}, {Left: 2, Right: 0}}, LeftWidth: 3, RightWidth: 3}
	orders := testkit.Rows([]any{1, 7, "eu"}, []any{2, 8, "eu"}, []any{3, 7, nil})
	got, err := IndexNestedLoopJoin(exec.Values(orders...), idx, spec)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, got, testkit.Rows(
		[]any{1, 7, "eu", "eu", 7, "anna"},
		[]any{1, 7, "eu", "eu", 7, "anna-2"},
	))
	if idx.Lookups != 2 {
		t.Fatalf("lookups = %d, want 2", idx.Lookups)
	}
}

func TestP8_12_IndexNestedLoopJoin_Errors(t *testing.T) {
	for _, jt := range []JoinType{Right, Full, Semi, Anti} {
		o := exec.Values(employees()...)
		if _, err := IndexNestedLoopJoin(o, NewFakeIndex(departments(), []int{0}), empDept(jt)); !errors.Is(err, ErrUnsupportedJoin) {
			t.Fatalf("%v: err = %v, want ErrUnsupportedJoin", jt, err)
		}
		if o.Opens != 0 {
			t.Fatalf("%v: outer opened before rejecting the join type", jt)
		}
	}
	f := &failIter{rows: employees()[:2]}
	if _, err := IndexNestedLoopJoin(f, NewFakeIndex(departments(), []int{0}), empDept(Inner)); !errors.Is(err, errBoom) {
		t.Fatalf("outer error: err = %v, want errBoom", err)
	}
	checkBalanced(t, "outer", f.Opens, f.Closes)
	o := exec.Values(employees()...)
	rows, err := IndexNestedLoopJoin(o, NewFakeIndex(departments(), []int{0}), withExtra(empDept(Left), failOnBob))
	if !errors.Is(err, errBoom) || rows != nil {
		t.Fatalf("extra error: rows=%v err=%v, want nil, errBoom", rows, err)
	}
	checkBalanced(t, "outer", o.Opens, o.Closes)
}

func TestP8_12_IndexNestedLoopJoin_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	nonNull := 0
	for _, l := range left {
		if !l[1].IsNull() {
			nonNull++
		}
	}
	for _, jt := range []JoinType{Inner, Left} {
		for _, extra := range []bool{false, true} {
			idx := NewFakeIndex(right, []int{1})
			got, err := IndexNestedLoopJoin(exec.Values(left...), idx, randSpec(jt, extra))
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, randOracle(jt, extra))
			if idx.Lookups != nonNull {
				t.Fatalf("lookups = %d, want %d", idx.Lookups, nonNull)
			}
		}
	}
}
