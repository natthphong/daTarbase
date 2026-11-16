package join

// ColStats are the planner statistics of one join column (Postgres:
// reltuples from pg_class, n_distinct and null_frac from pg_stats).
type ColStats struct {
	Rows     float64 // number of rows in the relation
	NDV      float64 // number of distinct non-NULL values of the column
	NullFrac float64 // fraction of rows whose value is NULL, in [0, 1]
}

// Problem 8.13 — EstimateEquiJoinRows [Easy]
//
// Estimate the row count of R ⨝ S ON R.a = S.b from table sizes and distinct counts.
//
// The classic System R estimate assumes "containment of value sets": every
// value of the column with fewer distinct values also appears in the other
// column. Then each non-NULL row of R finds |S|/ndv(S.b) partners on
// average, if ndv(R.a) <= ndv(S.b), and symmetrically otherwise:
//
//	rows = |R| · |S| · (1 - nullfrac(R.a)) · (1 - nullfrac(S.b)) / max(ndv(R.a), ndv(S.b))
//
// Rules:
//  1. Clamp each NDV into [1, Rows] first: an NDV below 1 counts as 1, an
//     NDV above the row count counts as the row count (a column cannot have
//     more distinct values than rows). If Rows < 1, the upper clamp does not
//     apply (NDV just becomes at least 1).
//  2. Apply the formula above with the clamped NDVs.
//  3. Like Postgres' clamp_row_est: if the result is <= 1, return 1 (the
//     planner never estimates 0 rows); otherwise round to the nearest
//     integer, halves to even (math.RoundToEven).
//
// Example:
//
//	R{Rows 1000, NDV 10}, S{Rows 1000, NDV 100}                → 10000
//	employees{10000, NDV 50, NullFrac 0.02}, departments{60, NDV 60} → 9800
//	R{7, NDV 3}, S{5, NDV 3}                                    → 11.67 → 12
//	R{10, NDV 500}, S{1000, NDV 100}  (500 clamped to 10)      → 100
//	R{3, NDV 0}, S{4, NDV 0}          (NDV 0 counts as 1)      → 12
//	R{1, NDV 1, NullFrac 0.5}, S{1, NDV 1}                      → 0.5 → 1
//	R{0, NDV 0}, S{100, NDV 10}                                 → 0 → 1
//
// Constraints: O(1).
// Postgres reference: eqjoinsel_inner() in src/backend/utils/adt/selfuncs.c, clamp_row_est() in costsize.c
func EstimateEquiJoinRows(r, s ColStats) float64 {
	panic("TODO: Problem 8.13")
}

// Problem 8.14 — BestJoinOrder [Hard]
//
// Find the cheapest left-deep join order of n tables with dynamic programming over table subsets (Selinger).
//
// A left-deep plan joins tables one at a time: ((t1 ⨝ t2) ⨝ t3) ⨝ t4. Its
// cost here is the total size of everything it produces:
//
//	cost(t1, ..., tn) = size({t1,t2}) + size({t1,t2,t3}) + ... + size({t1..tn})
//	size(S)           = Π card[i] for i in S  ×  Π sel[i][j] for i < j both in S
//
// sel[i][j] is the selectivity of the join predicate between tables i and j
// (1 = no predicate, i.e. a cross product). There are n! orders; but the
// cost of the best order ending in table t over set S is
// best(S \ {t}) + size(S) — only the SET joined so far matters, not its
// order. So compute best(S) for all 2^n subsets, smallest first.
//
// Rules:
//  1. n = len(card). n < 1 or n > 16 → (nil, 0, ErrBadInput). sel must have
//     n rows of length n, else ErrBadInput. Only sel[i][j] with i < j is
//     read (assume symmetry; the diagonal is ignored).
//  2. Return the order (a permutation of 0..n-1, first-joined first) with the
//     minimum cost, and that cost. n = 1 → ([0], 0, nil).
//  3. Ties: two costs a, b are equal if |a-b| <= 1e-9 · max(|a|, |b|). Among
//     equal-cost orders return the lexicographically smallest one (so the
//     first two tables are always in ascending order).
//  4. Cross products are allowed (they are just expensive).
//
// Example: card A=1000, B=10, C=50 (indexes 0, 1, 2);
// sel(A,B) = 0.01, sel(B,C) = 0.1, sel(A,C) = 1 (no predicate).
//
//	size{A,B} = 100, size{B,C} = 50, size{A,C} = 50000, size{A,B,C} = 500
//	[0 1 2] → 100 + 500 = 600        [0 2 1] → 50000 + 500 = 50500
//	[1 2 0] → 50  + 500 = 550  ✓     [2 1 0] → 550 (tie, larger lexicographically)
//	answer: [1 2 0], cost 550
//	card [5, 3], sel(0,1) = 0.5 → [0 1], cost 7.5
//
// Constraints: O(2^n · n²) time.
// Postgres reference: standard_join_search() / join_search_one_level() in src/backend/optimizer/path/allpaths.c, joinrels.c
func BestJoinOrder(card []float64, sel [][]float64) ([]int, float64, error) {
	panic("TODO: Problem 8.14")
}
