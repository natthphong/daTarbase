package sortagg

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP7_12_HashDistinct(t *testing.T) {
	tests := []struct {
		name string
		rows []types.Row
		want []types.Row
	}{
		{"duplicates and nulls",
			testkit.Rows([]any{1, "a"}, []any{2, "b"}, []any{1, "a"}, []any{nil, "x"}, []any{nil, "x"}, []any{1, "b"}),
			testkit.Rows([]any{1, "a"}, []any{2, "b"}, []any{nil, "x"}, []any{1, "b"})},
		{"non-adjacent duplicates removed", intRows(3, 1, 3, 2, 1, 3), intRows(3, 1, 2)},
		{"1 and 1.0 are the same", testkit.Rows([]any{1}, []any{1.0}), testkit.Rows([]any{1})},
		{"null vs value in same column differ", testkit.Rows([]any{nil, 1}, []any{1, nil}), testkit.Rows([]any{nil, 1}, []any{1, nil})},
		{"all distinct", intRows(1, 2, 3), intRows(1, 2, 3)},
		{"empty", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exec.Collect(NewHashDistinct(exec.Values(tc.rows...)))
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
		})
	}
}

func TestP7_12_HashDistinct_Streaming(t *testing.T) {
	child := exec.Values(testkit.Rows([]any{1, "a"}, []any{2, "b"}, []any{1, "a"}, []any{nil, "x"}, []any{nil, "x"}, []any{1, "b"})...)
	op := NewHashDistinct(child)
	if err := op.Open(); err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	pullN(t, op, 2)
	if child.Pulled != 2 {
		t.Fatalf("after 2 Next child pulled %d, want 2", child.Pulled)
	}
	pullN(t, op, 1) // skips the duplicate (1,'a')
	if child.Pulled != 4 {
		t.Fatalf("after 3 Next child pulled %d, want 4", child.Pulled)
	}
}

func TestP7_12_HashDistinct_ChildError(t *testing.T) {
	op := NewHashDistinct(&failingIter{rows: intRows(1, 1)})
	op.Open()
	pullN(t, op, 1)
	if _, _, err := op.Next(); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
}

func TestP7_13_Unique(t *testing.T) {
	tests := []struct {
		name string
		rows []types.Row
		want []types.Row
	}{
		{"sorted with nulls", testkit.Rows([]any{1}, []any{1}, []any{2}, []any{nil}, []any{nil}), testkit.Rows([]any{1}, []any{2}, []any{nil})},
		{"unsorted: non-adjacent duplicates survive", intRows(1, 2, 1), intRows(1, 2, 1)},
		{"multi-column", testkit.Rows([]any{1, "a"}, []any{1, "a"}, []any{1, "b"}, []any{2, "b"}, []any{2, "b"}),
			testkit.Rows([]any{1, "a"}, []any{1, "b"}, []any{2, "b"})},
		{"all equal", intRows(7, 7, 7, 7), intRows(7)},
		{"single row", intRows(5), intRows(5)},
		{"empty", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exec.Collect(NewUnique(exec.Values(tc.rows...)))
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
		})
	}
}

func TestP7_13_Unique_Streaming(t *testing.T) {
	child := exec.Values(intRows(1, 1, 1, 2, 3)...)
	op := NewUnique(child)
	if err := op.Open(); err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	pullN(t, op, 1)
	if child.Pulled != 1 {
		t.Fatalf("after first Next child pulled %d, want 1", child.Pulled)
	}
	pullN(t, op, 1)
	if child.Pulled != 4 {
		t.Fatalf("after second Next child pulled %d, want 4", child.Pulled)
	}
}

// Sort + Unique gives the same set as HashDistinct.
func TestP7_13_Unique_AfterSort(t *testing.T) {
	rows := intRows(5, 3, 5, 1, 3, 3, 9)
	got, err := exec.Collect(NewUnique(NewSort(exec.Values(rows...), Asc(0))))
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, got, intRows(1, 3, 5, 9))
}
