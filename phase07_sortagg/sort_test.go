package sortagg

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// (id, dept, salary)
func staffRows() []types.Row {
	return testkit.Rows(
		[]any{1, "ops", 80},
		[]any{2, "eng", 100},
		[]any{3, "ops", 90},
		[]any{4, "eng", 120},
		[]any{5, "eng", 100},
	)
}

func ids(rows []types.Row) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r[0].I
	}
	return out
}

func TestP7_2_Sort(t *testing.T) {
	tests := []struct {
		name    string
		rows    []types.Row
		keys    []SortKey
		wantIDs []int64
	}{
		{"stable on equal dept", staffRows(), []SortKey{Asc(1)}, []int64{2, 4, 5, 1, 3}},
		{"dept asc, salary desc", staffRows(), []SortKey{Asc(1), Desc(2)}, []int64{4, 2, 5, 3, 1}},
		{"salary asc, ties keep input order", staffRows(), []SortKey{Asc(2)}, []int64{1, 3, 2, 5, 4}},
		{"no keys keeps input order", staffRows(), nil, []int64{1, 2, 3, 4, 5}},
		{"empty input", nil, []SortKey{Asc(0)}, []int64{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			child := exec.Values(tc.rows...)
			got, err := exec.Collect(NewSort(child, tc.keys...))
			if err != nil {
				t.Fatal(err)
			}
			g := ids(got)
			if len(g) != len(tc.wantIDs) {
				t.Fatalf("ids = %v, want %v", g, tc.wantIDs)
			}
			for i := range g {
				if g[i] != tc.wantIDs[i] {
					t.Fatalf("ids = %v, want %v", g, tc.wantIDs)
				}
			}
			if child.Opens != 1 || child.Closes != 1 || child.Pulled != len(tc.rows) {
				t.Fatalf("child opens/closes/pulled = %d/%d/%d, want 1/1/%d", child.Opens, child.Closes, child.Pulled, len(tc.rows))
			}
		})
	}
}

func TestP7_2_Sort_Nulls(t *testing.T) {
	rows := testkit.Rows([]any{1, nil}, []any{2, "b"}, []any{3, nil}, []any{4, "a"})
	got, err := exec.Collect(NewSort(exec.Values(rows...), Asc(1)))
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, got, testkit.Rows([]any{4, "a"}, []any{2, "b"}, []any{1, nil}, []any{3, nil}))
	got, _ = exec.Collect(NewSort(exec.Values(rows...), Desc(1)))
	testkit.AssertRows(t, got, testkit.Rows([]any{1, nil}, []any{3, nil}, []any{2, "b"}, []any{4, "a"}))
}

func TestP7_2_Sort_ChildError(t *testing.T) {
	s := NewSort(&failingIter{rows: intRows(1, 2)}, Asc(0))
	if err := s.Open(); !errors.Is(err, errBoom) {
		t.Fatalf("Open err = %v, want %v", err, errBoom)
	}
}

// rows (i, i*37 % 100) for i = 0..999
func modRows() []types.Row {
	rows := make([]types.Row, 1000)
	for i := range rows {
		rows[i] = testkit.R(i, i*37%100)
	}
	return rows
}

func TestP7_3_TopN(t *testing.T) {
	tests := []struct {
		name     string
		rows     []types.Row
		n        int
		keys     []SortKey
		want     []types.Row
		wantPeak int
	}{
		{
			name: "smallest 5, ties broken by input order",
			rows: modRows(), n: 5, keys: []SortKey{Asc(1)},
			want:     testkit.Rows([]any{0, 0}, []any{100, 0}, []any{200, 0}, []any{300, 0}, []any{400, 0}),
			wantPeak: 5,
		},
		{
			name: "largest 3",
			rows: modRows(), n: 3, keys: []SortKey{Desc(1)},
			want:     testkit.Rows([]any{27, 99}, []any{127, 99}, []any{227, 99}),
			wantPeak: 3,
		},
		{
			name: "12 smallest cross a value boundary",
			rows: modRows(), n: 12, keys: []SortKey{Asc(1)},
			want: testkit.Rows(
				[]any{0, 0}, []any{100, 0}, []any{200, 0}, []any{300, 0}, []any{400, 0},
				[]any{500, 0}, []any{600, 0}, []any{700, 0}, []any{800, 0}, []any{900, 0},
				[]any{73, 1}, []any{173, 1}),
			wantPeak: 12,
		},
		{
			name: "all keys equal: first n input rows",
			rows: testkit.Rows([]any{1, "a"}, []any{2, "a"}, []any{3, "a"}, []any{4, "a"}),
			n:    2, keys: []SortKey{Asc(1)},
			want:     testkit.Rows([]any{1, "a"}, []any{2, "a"}),
			wantPeak: 2,
		},
		{
			name: "fewer rows than n",
			rows: testkit.Rows([]any{3}, []any{nil}, []any{1}, []any{2}),
			n:    10, keys: []SortKey{Asc(0)},
			want:     testkit.Rows([]any{1}, []any{2}, []any{3}, []any{nil}),
			wantPeak: 4,
		},
		{
			name: "nulls first in desc",
			rows: testkit.Rows([]any{3}, []any{nil}, []any{1}, []any{2}),
			n:    2, keys: []SortKey{Desc(0)},
			want:     testkit.Rows([]any{nil}, []any{3}),
			wantPeak: 2,
		},
		{
			name: "empty input",
			rows: nil, n: 3, keys: []SortKey{Asc(0)},
			want: nil, wantPeak: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			child := exec.Values(tc.rows...)
			op := NewTopN(child, tc.n, tc.keys...)
			got, err := exec.Collect(op)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
			if child.Pulled != len(tc.rows) {
				t.Fatalf("child pulled %d rows, want %d (whole input)", child.Pulled, len(tc.rows))
			}
			if op.Peak != tc.wantPeak {
				t.Fatalf("Peak = %d, want %d", op.Peak, tc.wantPeak)
			}
			if child.Opens != 1 || child.Closes != 1 {
				t.Fatalf("child opens/closes = %d/%d, want 1/1", child.Opens, child.Closes)
			}
		})
	}
}

func TestP7_3_TopN_LimitZero(t *testing.T) {
	child := exec.Values(intRows(5, 4, 3)...)
	op := NewTopN(child, 0, Asc(0))
	got, err := exec.Collect(op)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || child.Pulled != 0 || op.Peak != 0 {
		t.Fatalf("rows=%d pulled=%d peak=%d, want 0/0/0", len(got), child.Pulled, op.Peak)
	}
}

// The result must equal the first N rows of a stable full sort.
func TestP7_3_TopN_MatchesSort(t *testing.T) {
	rows := make([]types.Row, 300)
	for i := range rows {
		rows[i] = testkit.R(i, (i*7919)%13, (i*31)%5)
	}
	keys := []SortKey{Desc(2), Asc(1)}
	full, err := exec.Collect(NewSort(exec.Values(rows...), keys...))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 7, 50, 299, 300} {
		op := NewTopN(exec.Values(rows...), n, keys...)
		got, err := exec.Collect(op)
		if err != nil {
			t.Fatal(err)
		}
		testkit.AssertRows(t, got, full[:n])
		if op.Peak > n {
			t.Fatalf("n=%d: Peak = %d, must not exceed n", n, op.Peak)
		}
	}
}
