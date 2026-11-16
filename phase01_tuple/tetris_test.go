package tuple

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP1_15_ColumnTetris(t *testing.T) {
	tests := []struct {
		name      string
		schema    types.Schema
		wantOrder []int
		wantSaved int
	}{
		{"sample schema: 48 → 38 bytes", sampleSchema(), []int{2, 4, 0, 1, 3}, 10},
		{"bool/int8 interleaved", types.NewSchema("b1", types.KindBool, "i1", types.KindInt64, "b2", types.KindBool, "i2", types.KindInt64), []int{1, 3, 0, 2}, 14},
		{"texts move last", types.NewSchema("t1", types.KindText, "n", types.KindInt32, "t2", types.KindText, "f", types.KindBool), []int{1, 3, 0, 2}, 3},
		{"stable ties: int4s and 8-byte types", types.NewSchema("a", types.KindInt32, "b", types.KindInt64, "c", types.KindInt32, "d", types.KindFloat64), []int{1, 3, 0, 2}, 8},
		{"already optimal", types.NewSchema("a", types.KindInt64, "b", types.KindInt32, "c", types.KindBool), []int{0, 1, 2}, 0},
		{"reordering with nothing to save", types.NewSchema("a", types.KindInt32, "b", types.KindInt32, "c", types.KindInt64), []int{2, 0, 1}, 0},
		{"two bools before int4 waste 2", types.NewSchema("a", types.KindBool, "b", types.KindBool, "c", types.KindInt32), []int{2, 0, 1}, 2},
		{"single column", types.NewSchema("t", types.KindText), []int{0}, 0},
		{"empty schema", types.Schema{}, []int{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			order, saved := ColumnTetris(tc.schema)
			if order == nil || !slices.Equal(order, tc.wantOrder) {
				t.Fatalf("order = %v, want %v", order, tc.wantOrder)
			}
			if saved != tc.wantSaved {
				t.Fatalf("saved = %d, want %d", saved, tc.wantSaved)
			}
		})
	}
}

// The reordered layout never needs padding between fixed columns, so saved
// equals all padding of the original layout, and is never negative.
func TestP1_15_ColumnTetris_RandomSchemas(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 15))
	kinds := []types.Kind{types.KindBool, types.KindInt32, types.KindInt64, types.KindFloat64, types.KindText}
	for trial := 0; trial < 200; trial++ {
		var s types.Schema
		raw := 0 // bytes of the model row's values without any padding
		for i := 0; i < 1+r.IntN(12); i++ {
			k := kinds[r.IntN(len(kinds))]
			s.Columns = append(s.Columns, types.Column{Name: "c", Kind: k})
			if AttLen(k) > 0 {
				raw += AttLen(k)
			} else {
				raw++
			}
		}
		order, saved := ColumnTetris(s)
		if len(order) != s.Len() {
			t.Fatalf("schema %v: order %v has wrong length", s, order)
		}
		seen := map[int]bool{}
		for _, i := range order {
			seen[i] = true
		}
		if len(seen) != s.Len() {
			t.Fatalf("schema %v: order %v is not a permutation", s, order)
		}
		// Original model size = 16 (hoff) + raw + padding; new size has no padding.
		row := make(types.Row, s.Len())
		for i, c := range s.Columns {
			switch c.Kind {
			case types.KindText:
				row[i] = types.Text("")
			case types.KindBool:
				row[i] = types.Bool(false)
			case types.KindInt32:
				row[i] = types.Int32(0)
			default:
				row[i] = types.Value{Kind: c.Kind}
			}
		}
		padding := ComputeTupleSize(s, row) - 16 - raw
		if saved != padding || saved < 0 {
			t.Fatalf("schema %v: saved = %d, want all original padding %d", s.Columns, saved, padding)
		}
	}
}

func TestP1_15_ColumnTetris_DoesNotModifySchema(t *testing.T) {
	s := sampleSchema()
	before := slices.Clone(s.Columns)
	ColumnTetris(s)
	if !slices.Equal(s.Columns, before) {
		t.Fatalf("schema modified: %v, want %v", s.Columns, before)
	}
}
