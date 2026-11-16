package sortagg

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func abRows() []types.Row {
	return testkit.Rows(
		[]any{1, 3, "x"}, []any{1, 1, "y"}, []any{1, 2, "z"},
		[]any{2, 9, "p"}, []any{2, 8, "q"},
		[]any{3, 1, "r"},
	)
}

func TestP7_14_IncrementalSort(t *testing.T) {
	tests := []struct {
		name      string
		rows      []types.Row
		presorted int
		keys      []SortKey
		want      []types.Row
	}{
		{"order by a, b", abRows(), 1, []SortKey{Asc(0), Asc(1)},
			testkit.Rows([]any{1, 1, "y"}, []any{1, 2, "z"}, []any{1, 3, "x"}, []any{2, 8, "q"}, []any{2, 9, "p"}, []any{3, 1, "r"})},
		{"order by a, b desc", abRows(), 1, []SortKey{Asc(0), Desc(1)},
			testkit.Rows([]any{1, 3, "x"}, []any{1, 2, "z"}, []any{1, 1, "y"}, []any{2, 9, "p"}, []any{2, 8, "q"}, []any{3, 1, "r"})},
		{"ties on b keep input order",
			testkit.Rows([]any{1, 2, "first"}, []any{1, 1, "m"}, []any{1, 2, "second"}),
			1, []SortKey{Asc(0), Asc(1)},
			testkit.Rows([]any{1, 1, "m"}, []any{1, 2, "first"}, []any{1, 2, "second"})},
		{"null prefix group last (asc)",
			testkit.Rows([]any{1, 5, "a"}, []any{nil, 2, "b"}, []any{nil, 1, "c"}),
			1, []SortKey{Asc(0), Asc(1)},
			testkit.Rows([]any{1, 5, "a"}, []any{nil, 1, "c"}, []any{nil, 2, "b"})},
		{"presorted 0 = full sort", abRows(), 0, []SortKey{Asc(1)},
			testkit.Rows([]any{1, 1, "y"}, []any{3, 1, "r"}, []any{1, 2, "z"}, []any{1, 3, "x"}, []any{2, 8, "q"}, []any{2, 9, "p"})},
		{"presorted = all keys: passthrough", abRows(), 1, []SortKey{Asc(0)}, abRows()},
		{"empty", nil, 1, []SortKey{Asc(0), Asc(1)}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exec.Collect(NewIncrementalSort(exec.Values(tc.rows...), tc.presorted, tc.keys...))
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
		})
	}
}

func TestP7_14_IncrementalSort_Streaming(t *testing.T) {
	child := exec.Values(abRows()...)
	op := NewIncrementalSort(child, 1, Asc(0), Asc(1))
	if err := op.Open(); err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	// Group a=1 (3 rows) + one look-ahead row, then a=2 (2 rows) + look-ahead, then a=3.
	steps := []struct{ next, wantPulled int }{{1, 4}, {2, 4}, {1, 6}, {1, 6}, {1, 6}}
	for i, s := range steps {
		pullN(t, op, s.next)
		if child.Pulled != s.wantPulled {
			t.Fatalf("step %d: child pulled %d, want %d", i, child.Pulled, s.wantPulled)
		}
	}
	if _, ok, err := op.Next(); ok || err != nil {
		t.Fatalf("Next after end: ok=%v err=%v, want false/nil", ok, err)
	}
}

// Same result as a full Sort when the prefix is truly sorted.
func TestP7_14_IncrementalSort_MatchesSort(t *testing.T) {
	var rows []types.Row
	for a := 0; a < 10; a++ {
		for j := 0; j < 7; j++ {
			rows = append(rows, testkit.R(a, (a*13+j*5)%7, j))
		}
	}
	keys := []SortKey{Asc(0), Desc(1)}
	want, _ := exec.Collect(NewSort(exec.Values(rows...), keys...))
	got, err := exec.Collect(NewIncrementalSort(exec.Values(rows...), 1, keys...))
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, got, want)
}

func TestP7_14_IncrementalSort_ChildError(t *testing.T) {
	op := NewIncrementalSort(&failingIter{rows: intRows(1, 1)}, 1, Asc(0))
	op.Open()
	if _, _, err := op.Next(); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
}
