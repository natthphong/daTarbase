package join

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP8_1_Match(t *testing.T) {
	multi := JoinSpec{Keys: []KeyPair{{Left: 0, Right: 1}, {Left: 1, Right: 0}}}
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right types.Row
		want        bool
	}{
		{"equal keys", empDept(Inner), testkit.R(1, "alice", 10), testkit.R(10, "eng"), true},
		{"different keys", empDept(Inner), testkit.R(5, "erin", 40), testkit.R(10, "eng"), false},
		{"NULL left key", empDept(Inner), testkit.R(4, "dave", nil), testkit.R(10, "eng"), false},
		{"NULL right key", empDept(Inner), testkit.R(1, "alice", 10), testkit.R(nil, "unassigned"), false},
		{"NULL = NULL is not a match", empDept(Inner), testkit.R(4, "dave", nil), testkit.R(nil, "unassigned"), false},
		{"int4 equals int8", empDept(Inner), testkit.R(1, "alice", int32(10)), testkit.R(10, "eng"), true},
		{"int equals float", empDept(Inner), testkit.R(1, "alice", 10), testkit.R(10.0, "eng"), true},
		{"multi-key all equal", multi, testkit.R(1, "x"), testkit.R("x", 1), true},
		{"multi-key second differs", multi, testkit.R(1, "x"), testkit.R("y", 1), false},
		{"multi-key NULL in second", multi, testkit.R(1, nil), testkit.R(nil, 1), false},
		{"no keys = cross join", JoinSpec{}, testkit.R(1), testkit.R(2), true},
		{"extra true", withExtra(empDept(Inner), notEmea), testkit.R(2, "bob", 20), testkit.R(20, "sales"), true},
		{"extra false", withExtra(empDept(Inner), notEmea), testkit.R(2, "bob", 20), testkit.R(20, "sales-emea"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.spec.Match(tc.left, tc.right)
			if err != nil {
				t.Fatalf("Match(%v, %v) error: %v", tc.left, tc.right, err)
			}
			if got != tc.want {
				t.Fatalf("Match(%v, %v) = %v, want %v", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func TestP8_1_Match_ExtraSeesCombinedRowOnlyWhenKeysMatch(t *testing.T) {
	var seen []types.Row
	spec := withExtra(empDept(Inner), func(r types.Row) (bool, error) {
		seen = append(seen, r)
		return true, nil
	})
	l, r := testkit.R(2, "bob", 20), testkit.R(20, "sales-emea")
	if ok, _ := spec.Match(l, r); !ok {
		t.Fatalf("Match = false, want true")
	}
	if _, _ = spec.Match(testkit.R(5, "erin", 40), r); len(seen) != 1 {
		t.Fatalf("Extra called %d times, want 1 (not called when keys differ)", len(seen))
	}
	if _, _ = spec.Match(testkit.R(4, "dave", nil), testkit.R(nil, "x")); len(seen) != 1 {
		t.Fatalf("Extra called %d times, want 1 (not called for NULL keys)", len(seen))
	}
	testkit.AssertRows(t, seen, []types.Row{testkit.R(2, "bob", 20, 20, "sales-emea")})
	testkit.AssertRows(t, []types.Row{l, r}, []types.Row{testkit.R(2, "bob", 20), testkit.R(20, "sales-emea")})
}

func TestP8_1_Match_ExtraError(t *testing.T) {
	spec := withExtra(empDept(Inner), failOnBob)
	if _, err := spec.Match(testkit.R(2, "bob", 20), testkit.R(20, "sales")); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	if ok, err := spec.Match(testkit.R(1, "alice", 10), testkit.R(10, "eng")); !ok || err != nil {
		t.Fatalf("Match = %v, %v; want true, nil", ok, err)
	}
}
