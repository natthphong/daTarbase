package sortagg

import (
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	}
	return 0
}

func TestP7_1_CompareRows(t *testing.T) {
	tests := []struct {
		name string
		a, b types.Row
		keys []SortKey
		want int
	}{
		{"asc smaller first", testkit.R(1), testkit.R(2), []SortKey{Asc(0)}, -1},
		{"asc larger after", testkit.R(5), testkit.R(2), []SortKey{Asc(0)}, 1},
		{"desc inverts", testkit.R(1), testkit.R(2), []SortKey{Desc(0)}, 1},
		{"equal", testkit.R(7, "x"), testkit.R(7, "y"), []SortKey{Asc(0)}, 0},
		{"no keys means equal", testkit.R(1), testkit.R(2), nil, 0},
		{"second key decides", testkit.R(1, "b"), testkit.R(1, "a"), []SortKey{Asc(0), Asc(1)}, 1},
		{"second key desc", testkit.R(1, "b"), testkit.R(1, "a"), []SortKey{Asc(0), Desc(1)}, -1},
		{"first key wins over second", testkit.R(1, "z"), testkit.R(2, "a"), []SortKey{Asc(0), Asc(1)}, -1},
		{"int vs float numeric", testkit.R(2), testkit.R(1.5), []SortKey{Asc(0)}, 1},
		{"text bytewise", testkit.R("B"), testkit.R("a"), []SortKey{Asc(0)}, -1},
		{"null vs null equal", testkit.R(nil), testkit.R(nil), []SortKey{Asc(0)}, 0},
		{"null vs null then next key", testkit.R(nil, 2), testkit.R(nil, 1), []SortKey{Asc(0), Asc(1)}, 1},
		{"asc default: null last", testkit.R(nil), testkit.R(1), []SortKey{Asc(0)}, 1},
		{"asc default: value before null", testkit.R(1), testkit.R(nil), []SortKey{Asc(0)}, -1},
		{"desc default: null first", testkit.R(nil), testkit.R(1), []SortKey{Desc(0)}, -1},
		{"asc nulls first", testkit.R(nil), testkit.R(1), []SortKey{{Col: 0, NullsFirst: true}}, -1},
		{"desc nulls last", testkit.R(nil), testkit.R(1), []SortKey{{Col: 0, Desc: true}}, 1},
		{"desc nulls last, value side", testkit.R(1), testkit.R(nil), []SortKey{{Col: 0, Desc: true}}, -1},
		{"key on later column", testkit.R(9, 1), testkit.R(1, 2), []SortKey{Asc(1)}, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sign(CompareRows(tc.a, tc.b, tc.keys)); got != tc.want {
				t.Fatalf("CompareRows(%v, %v) sign = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			// Antisymmetry.
			if got := sign(CompareRows(tc.b, tc.a, tc.keys)); got != -tc.want {
				t.Fatalf("CompareRows(%v, %v) sign = %d, want %d", tc.b, tc.a, got, -tc.want)
			}
		})
	}
}

func TestP7_1_CompareRows_NullPlacement(t *testing.T) {
	input := testkit.Rows([]any{3}, []any{nil}, []any{1}, []any{2})
	tests := []struct {
		name string
		key  SortKey
		want []types.Row
	}{
		{"ASC (nulls last)", Asc(0), testkit.Rows([]any{1}, []any{2}, []any{3}, []any{nil})},
		{"DESC (nulls first)", Desc(0), testkit.Rows([]any{nil}, []any{3}, []any{2}, []any{1})},
		{"ASC NULLS FIRST", SortKey{Col: 0, NullsFirst: true}, testkit.Rows([]any{nil}, []any{1}, []any{2}, []any{3})},
		{"DESC NULLS LAST", SortKey{Col: 0, Desc: true}, testkit.Rows([]any{3}, []any{2}, []any{1}, []any{nil})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := slices.Clone(input)
			slices.SortStableFunc(rows, func(a, b types.Row) int { return CompareRows(a, b, []SortKey{tc.key}) })
			testkit.AssertRows(t, rows, tc.want)
		})
	}
}

func TestP7_1_KeyDefaults(t *testing.T) {
	if got, want := Asc(2), (SortKey{Col: 2}); got != want {
		t.Fatalf("Asc(2) = %+v, want %+v", got, want)
	}
	if got, want := Desc(1), (SortKey{Col: 1, Desc: true, NullsFirst: true}); got != want {
		t.Fatalf("Desc(1) = %+v, want %+v", got, want)
	}
}
