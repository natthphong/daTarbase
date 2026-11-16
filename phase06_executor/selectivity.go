package executor

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// ColumnStats is what ANALYZE stores for one column (Postgres: pg_statistic,
// readable through the pg_stats view).
//
// Fractions are of ALL rows of the table (NULLs included).
type ColumnStats struct {
	RowCount  float64       // rows in the table (pg_class.reltuples)
	NullFrac  float64       // fraction of rows where the column is NULL (null_frac)
	NDistinct float64       // > 0: number of distinct non-NULL values; < 0: minus the ratio distinct/rows (e.g. -0.05 = 5% of rows); 0: unknown (n_distinct)
	MCVs      []types.Value // most common values, most common first (most_common_vals)
	MCVFreqs  []float64     // frequency of each MCV, same order (most_common_freqs)
	Histogram []types.Value // ascending equi-depth bucket bounds over the non-NULL, non-MCV values (histogram_bounds); fewer than 2 = no histogram
}

const (
	// DefaultNumDistinct is assumed when NDistinct is 0 (Postgres: DEFAULT_NUM_DISTINCT).
	DefaultNumDistinct = 200.0
	// DefaultRangeIneqSel is used for a nonsensical range (Postgres: DEFAULT_RANGE_INEQ_SEL).
	DefaultRangeIneqSel = 0.005
)

// Problem 6.11 — EqSel [Medium]
//
// Estimate the fraction of rows satisfying "col = c" from the column statistics.
//
// Postgres' var_eq_const: if c is a most-common value, its stored frequency is
// the answer. Otherwise assume the remaining (non-NULL, non-MCV) rows are spread
// evenly over the remaining distinct values.
//
// Rules:
//  1. c is NULL → 0 ("col = NULL" is never TRUE).
//  2. If c equals MCVs[i] (types.Compare == 0) → MCVFreqs[i].
//  3. Otherwise:
//     nd       = NDistinct            if NDistinct > 0
//     = -NDistinct * RowCount         if NDistinct < 0
//     = DefaultNumDistinct (200)      if NDistinct == 0
//     sel      = 1 - sum(MCVFreqs) - NullFrac, clamped to [0, 1]
//     other    = nd - len(MCVs)
//     if other > 1: sel = sel / other          (no division when other ≤ 1)
//     if there are MCVs and sel > the LAST (least common) MCV frequency:
//     sel = that frequency   (a non-MCV can't be more common than an MCV)
//  4. Result is in [0, 1].
//
// Example: RowCount 1000, NullFrac 0.1, NDistinct 52, MCVs [5, 7] freqs [0.2, 0.1]
//
//	c = 5    → 0.2
//	c = 12   → (1 - 0.3 - 0.1) / (52 - 2) = 0.012
//	c = NULL → 0
//	same stats but NDistinct 3 → c = 12: other = 1 → sel 0.6 → capped to 0.1
//	NDistinct -0.05 → nd = 50 → c = 12: 0.6 / 48 = 0.0125
//
// Constraints: O(len(MCVs)).
// Postgres reference: var_eq_const() in src/backend/utils/adt/selfuncs.c
func EqSel(s ColumnStats, c types.Value) float64 {
	if c.IsNull() {
		return 0
	}
	sum := 0.0
	for i, m := range s.MCVs {
		if types.Compare(m, c) == 0 {
			return s.MCVFreqs[i]
		}
		sum += s.MCVFreqs[i]
	}
	nd := s.NDistinct
	switch {
	case nd < 0:
		nd = -nd * s.RowCount
	case nd == 0:
		nd = DefaultNumDistinct
	}
	sel := 1 - sum - s.NullFrac
	sel = max(0, min(1, sel))
	if other := nd - float64(len(s.MCVs)); other > 1 {
		sel /= other
	}
	if n := len(s.MCVFreqs); n > 0 && sel > s.MCVFreqs[n-1] {
		sel = s.MCVFreqs[n-1]
	}
	return max(0, min(1, sel))
}

// Problem 6.12 — IneqSel [Hard]
//
// Estimate the fraction of rows satisfying "col op c" for op in <, <=, >, >= using MCVs plus an equi-depth histogram.
//
// The population splits into three disjoint parts:
//
//	NULLs (NullFrac) | MCVs (sum of MCVFreqs) | the rest, described by the histogram
//
// MCVs are tested exactly. The rest is described by an equi-depth histogram:
// bounds b0 < … ≤ bn delimit n buckets, each holding 1/n of "the rest".
//
//	bounds  0     10     20     30     40
//	        |--1/4--|--1/4--|--1/4--|--1/4--|
//	c = 15 falls half-way into bucket 1  →  fraction below c = (1 + 0.5) / 4 = 0.375
//
// Rules:
//  1. c NULL → 0. op must be OpLT, OpLE, OpGT or OpGE (anything else → 0).
//  2. mcvSel = sum of MCVFreqs[i] for which "MCVs[i] op c" is true; sumMCV = sum of all MCVFreqs.
//  3. below = the fraction of the histogram population below c (the same value is
//     used for < and <= — the histogram is treated as continuous):
//     - c < b0 → 0;  c ≥ bn → 1;
//     - else let i be the LARGEST index in [0, n) with b_i ≤ c (so b_i ≤ c < b_{i+1});
//     frac = (c - b_i) / (b_{i+1} - b_i) when the values are numeric (as float64),
//     frac = 0.5 for any other type;  below = (i + frac) / n.
//     For < and <= histSel = below; for > and >= histSel = 1 - below.
//     With fewer than 2 bounds there is no histogram: histSel = 0.5.
//  4. sel = (1 - NullFrac - sumMCV) * histSel + mcvSel, clamped to [0, 1].
//
// Example (RowCount 1000, NullFrac 0.1, MCVs [5, 7] freqs [0.2, 0.1], bounds [0 10 20 30 40]):
//
//	col <  15 → 0.6 * 0.375 + 0.3   = 0.525
//	col <   5 → 0.6 * 0.125 + 0     = 0.075
//	col <=  5 → 0.6 * 0.125 + 0.2   = 0.275
//	col >= 7  → 0.6 * (1 - 0.175) + 0.1 = 0.595
//	col <  -3 → 0;  col < 100 → 0.9
//
// Constraints: O(len(MCVs) + log len(Histogram)) — binary search the bounds.
// Postgres reference: scalarineqsel(), ineq_histogram_selectivity(), mcv_selectivity() in selfuncs.c
// (Postgres also clamps tiny histogram fractions to 0.01/(n) — we don't.)
func IneqSel(s ColumnStats, op CmpOp, c types.Value) float64 {
	if c.IsNull() {
		return 0
	}
	if op != OpLT && op != OpLE && op != OpGT && op != OpGE {
		return 0
	}
	holds := func(v types.Value) bool {
		r := types.Compare(v, c)
		switch op {
		case OpLT:
			return r < 0
		case OpLE:
			return r <= 0
		case OpGT:
			return r > 0
		}
		return r >= 0
	}
	mcvSel, sumMCV := 0.0, 0.0
	for i, m := range s.MCVs {
		sumMCV += s.MCVFreqs[i]
		if holds(m) {
			mcvSel += s.MCVFreqs[i]
		}
	}
	histSel := 0.5
	if h := s.Histogram; len(h) >= 2 {
		n := len(h) - 1
		var below float64
		switch {
		case types.Compare(c, h[0]) < 0:
			below = 0
		case types.Compare(c, h[n]) >= 0:
			below = 1
		default:
			lo, hi := 0, n // invariant: h[lo] <= c < h[hi]
			for hi-lo > 1 {
				mid := (lo + hi) / 2
				if types.Compare(h[mid], c) <= 0 {
					lo = mid
				} else {
					hi = mid
				}
			}
			frac := 0.5
			num := func(v types.Value) (float64, bool) {
				switch v.Kind {
				case types.KindInt32, types.KindInt64:
					return float64(v.I), true
				case types.KindFloat64:
					return v.F, true
				}
				return 0, false
			}
			a, ok1 := num(h[lo])
			b, ok2 := num(h[lo+1])
			x, ok3 := num(c)
			if ok1 && ok2 && ok3 {
				frac = (x - a) / (b - a)
			}
			below = (float64(lo) + frac) / float64(n)
		}
		if op == OpLT || op == OpLE {
			histSel = below
		} else {
			histSel = 1 - below
		}
	}
	sel := (1-s.NullFrac-sumMCV)*histSel + mcvSel
	return max(0, min(1, sel))
}

// Problem 6.13 — BetweenSel [Easy]
//
// Estimate "col BETWEEN lo AND hi" by combining the two one-sided estimates the way Postgres combines a range pair.
//
// The planner sees `col >= lo AND col <= hi` as two clauses. Multiplying them
// (as if independent) would be badly wrong; Postgres instead recognises the pair
// and computes  sel = sel(col <= hi) + sel(col >= lo) - 1  — each side excludes
// what the other includes, so the overlap is what remains. NULLs were excluded
// by BOTH sides, so NullFrac is added back once.
//
// Rules:
//  1. lo or hi NULL → 0.
//  2. sel = IneqSel(<=, hi) + IneqSel(>=, lo) - 1 + NullFrac.
//  3. If sel ≤ 0: sel < -0.01 means the estimates are contradictory (e.g. lo > hi)
//     → DefaultRangeIneqSel (0.005); otherwise (just rounding noise) → 1e-10.
//  4. Clamp to at most 1.
//
// Example (same stats as Problem 6.12):
//
//	BETWEEN 10 AND 20 → 0.6 + 0.45 - 1 + 0.1 = 0.15
//	BETWEEN  5 AND  7 → 0.405 + 0.825 - 1 + 0.1 = 0.33   (= 0.2 + 0.1 MCVs + 0.03 histogram)
//	BETWEEN 30 AND 10 → 0.45 + 0.15 - 1 + 0.1 = -0.3 → 0.005
//
// Constraints: two calls to IneqSel.
// Postgres reference: clauselist_selectivity_ext() / addRangeClause() in src/backend/optimizer/path/clausesel.c
func BetweenSel(s ColumnStats, lo, hi types.Value) float64 {
	if lo.IsNull() || hi.IsNull() {
		return 0
	}
	sel := IneqSel(s, OpLE, hi) + IneqSel(s, OpGE, lo) - 1 + s.NullFrac
	if sel <= 0 {
		if sel < -0.01 {
			return DefaultRangeIneqSel
		}
		return 1e-10
	}
	return min(1, sel)
}
