package executor

import (
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// 1000 rows: 10% NULL, MCVs 5 (20%) and 7 (10%), 52 distinct values,
// the remaining 60% spread over the histogram 0 | 10 | 20 | 30 | 40.
func exampleStats() ColumnStats {
	return ColumnStats{
		RowCount:  1000,
		NullFrac:  0.1,
		NDistinct: 52,
		MCVs:      []types.Value{v(5), v(7)},
		MCVFreqs:  []float64{0.2, 0.1},
		Histogram: []types.Value{v(0), v(10), v(20), v(30), v(40)},
	}
}

func TestP6_11_EqSel(t *testing.T) {
	tests := []struct {
		name  string
		stats func(s *ColumnStats)
		c     types.Value
		want  float64
	}{
		{"most common value", nil, v(5), 0.2},
		{"second MCV", nil, v(7), 0.1},
		{"MCV matched across int kinds", nil, types.Int32(5), 0.2},
		{"non-MCV value: spread over the other distinct values", nil, v(12), 0.6 / 50},
		{"NULL constant", nil, v(nil), 0},
		{"few other distinct values: capped by the least common MCV", func(s *ColumnStats) { s.NDistinct = 3 }, v(12), 0.1},
		{"negative n_distinct is a ratio of rows", func(s *ColumnStats) { s.NDistinct = -0.05 }, v(12), 0.6 / 48},
		{"unknown n_distinct uses 200", func(s *ColumnStats) { s.NDistinct = 0 }, v(12), 0.6 / 198},
		{"no MCVs: uniform", func(s *ColumnStats) {
			s.MCVs, s.MCVFreqs, s.NullFrac, s.NDistinct = nil, nil, 0, 1000
		}, v(12), 0.001},
		{"unique column, no MCVs, with NULLs", func(s *ColumnStats) {
			s.MCVs, s.MCVFreqs, s.NDistinct = nil, nil, -0.9
		}, v(3), 0.9 / 900},
		{"MCVs cover everything", func(s *ColumnStats) {
			s.NullFrac, s.MCVFreqs, s.NDistinct = 0, []float64{0.7, 0.3}, 2
		}, v(9), 0},
		{"inconsistent stats clamp to 0", func(s *ColumnStats) {
			s.NullFrac = 0.8
		}, v(12), 0},
		{"single distinct non-MCV value", func(s *ColumnStats) {
			s.MCVs, s.MCVFreqs, s.NullFrac, s.NDistinct = nil, nil, 0.25, 1
		}, v(1), 0.75},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := exampleStats()
			if tc.stats != nil {
				tc.stats(&s)
			}
			if got := EqSel(s, tc.c); !near(got, tc.want) {
				t.Fatalf("EqSel(%s) = %v, want %v", tc.c, got, tc.want)
			}
		})
	}
}

func TestP6_12_IneqSel(t *testing.T) {
	tests := []struct {
		name string
		op   CmpOp
		c    types.Value
		want float64
	}{
		{"< 15: mid-bucket interpolation", OpLT, v(15), 0.6*0.375 + 0.3},
		{"< 5: MCV 5 excluded", OpLT, v(5), 0.6 * 0.125},
		{"<= 5: MCV 5 included", OpLE, v(5), 0.6*0.125 + 0.2},
		{"> 15", OpGT, v(15), 0.6 * 0.625},
		{">= 7: MCV 7 included", OpGE, v(7), 0.6*0.825 + 0.1},
		{"> 7: MCV 7 excluded", OpGT, v(7), 0.6 * 0.825},
		{"on a bucket bound", OpLT, v(20), 0.6*0.5 + 0.3},
		{"on the first bound", OpLT, v(0), 0},
		{"on the last bound", OpLT, v(40), 0.9},
		{"below the histogram", OpLT, v(-3), 0},
		{"above the histogram", OpLT, v(100), 0.9},
		{"> above the histogram", OpGT, v(100), 0},
		{">= below the histogram", OpGE, v(-3), 0.9},
		{"float constant", OpLT, v(32.5), 0.6*((3+0.25)/4) + 0.3},
		{"NULL constant", OpLT, v(nil), 0},
		{"= is not an inequality", OpEQ, v(15), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IneqSel(exampleStats(), tc.op, tc.c); !near(got, tc.want) {
				t.Fatalf("IneqSel(%s %s) = %v, want %v", tc.op, tc.c, got, tc.want)
			}
		})
	}
}

func TestP6_12_IneqSel_HistogramShapes(t *testing.T) {
	tests := []struct {
		name  string
		stats ColumnStats
		op    CmpOp
		c     types.Value
		want  float64
	}{
		{"no histogram: half of the rest", ColumnStats{NullFrac: 0.1, MCVs: []types.Value{v(5)}, MCVFreqs: []float64{0.3}, Histogram: []types.Value{v(1)}}, OpLT, v(9), 0.6*0.5 + 0.3},
		{"no stats at all but a histogram", ColumnStats{Histogram: []types.Value{v(0), v(100)}}, OpLT, v(25), 0.25},
		{"skewed buckets", ColumnStats{Histogram: []types.Value{v(0), v(1), v(2), v(1000)}}, OpLT, v(501), (2 + 0.5) / 3},
		{"duplicate bounds: pick the last bucket starting at or below c", ColumnStats{Histogram: []types.Value{v(0), v(10), v(10), v(20)}}, OpLT, v(10), 2.0 / 3},
		{"duplicate bounds, just below", ColumnStats{Histogram: []types.Value{v(0), v(10), v(10), v(20)}}, OpLT, v(5), 0.5 / 3},
		{"text histogram uses half a bucket", ColumnStats{Histogram: []types.Value{v("a"), v("m"), v("z")}}, OpLT, v("q"), 0.75},
		{"text below", ColumnStats{Histogram: []types.Value{v("b"), v("m"), v("z")}}, OpGE, v("a"), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IneqSel(tc.stats, tc.op, tc.c); !near(got, tc.want) {
				t.Fatalf("IneqSel(%s %s) = %v, want %v", tc.op, tc.c, got, tc.want)
			}
		})
	}
}

// Many bounds: the answer must come from a binary search and stay exact.
func TestP6_12_IneqSel_ManyBuckets(t *testing.T) {
	var h []types.Value
	for i := 0; i <= 100; i++ {
		h = append(h, v(i*10)) // 100 buckets of width 10
	}
	s := ColumnStats{RowCount: 1e6, Histogram: h}
	for _, c := range []int{0, 5, 123, 555, 999, 1000} {
		want := float64(c) / 1000
		if got := IneqSel(s, OpLT, v(c)); !near(got, want) {
			t.Fatalf("IneqSel(< %d) = %v, want %v", c, got, want)
		}
		if got := IneqSel(s, OpGE, v(c)); !near(got, 1-want) {
			t.Fatalf("IneqSel(>= %d) = %v, want %v", c, got, 1-want)
		}
	}
}

func TestP6_13_BetweenSel(t *testing.T) {
	tests := []struct {
		name   string
		lo, hi types.Value
		want   float64
	}{
		{"one histogram bucket", v(10), v(20), 0.15},
		{"both MCVs plus a sliver of histogram", v(5), v(7), 0.33},
		{"whole range", v(-100), v(100), 0.9},
		{"lo > hi is contradictory", v(30), v(10), DefaultRangeIneqSel},
		{"empty but consistent: tiny", v(41), v(50), 1e-10},
		{"NULL bound", v(nil), v(10), 0},
		{"NULL upper bound", v(1), v(nil), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := BetweenSel(exampleStats(), tc.lo, tc.hi); !near(got, tc.want) {
				t.Fatalf("BetweenSel(%s, %s) = %v, want %v", tc.lo, tc.hi, got, tc.want)
			}
		})
	}
}

func TestP6_11_EqSel_TextColumn(t *testing.T) {
	s := ColumnStats{
		RowCount: 500, NullFrac: 0, NDistinct: 12,
		MCVs:     []types.Value{v("TH"), v("US"), v("JP")},
		MCVFreqs: []float64{0.5, 0.2, 0.1},
	}
	tests := []struct {
		c    string
		want float64
	}{
		{"TH", 0.5}, {"US", 0.2}, {"JP", 0.1},
		{"FR", 0.2 / 9}, // 9 other countries share the remaining 20%
		{"th", 0.2 / 9}, // byte-wise compare: not an MCV
	}
	for _, tc := range tests {
		if got := EqSel(s, v(tc.c)); !near(got, tc.want) {
			t.Fatalf("EqSel('%s') = %v, want %v", tc.c, got, tc.want)
		}
	}
}

// With consistent statistics, the estimates for every distinct value add up to
// the non-NULL fraction of the table.
func TestP6_11_EqSel_SumsToNonNull(t *testing.T) {
	s := exampleStats() // 52 distinct: MCVs 5, 7 + 50 others
	total := EqSel(s, v(5)) + EqSel(s, v(7))
	for i := 0; i < 50; i++ {
		total += EqSel(s, v(1000+i))
	}
	if !near(total, 1-s.NullFrac) {
		t.Fatalf("sum of EqSel over all values = %v, want %v", total, 1-s.NullFrac)
	}
}
