package join

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestP8_13_EstimateEquiJoinRows(t *testing.T) {
	tests := []struct {
		name string
		r, s ColStats
		want float64
	}{
		{"divide by the larger NDV", ColStats{Rows: 1000, NDV: 10}, ColStats{Rows: 1000, NDV: 100}, 10000},
		{"symmetric", ColStats{Rows: 1000, NDV: 100}, ColStats{Rows: 1000, NDV: 10}, 10000},
		{"employees ⨝ departments with 2% NULL dept", ColStats{Rows: 10000, NDV: 50, NullFrac: 0.02}, ColStats{Rows: 60, NDV: 60}, 9800},
		{"rounded to nearest", ColStats{Rows: 7, NDV: 3}, ColStats{Rows: 5, NDV: 3}, 12},
		{"NDV clamped to row count", ColStats{Rows: 10, NDV: 500}, ColStats{Rows: 1000, NDV: 100}, 100},
		{"NDV 0 counts as 1", ColStats{Rows: 3, NDV: 0}, ColStats{Rows: 4, NDV: 0}, 12},
		{"never below one row", ColStats{Rows: 1, NDV: 1, NullFrac: 0.5}, ColStats{Rows: 1, NDV: 1}, 1},
		{"empty relation still estimates 1", ColStats{Rows: 0, NDV: 0}, ColStats{Rows: 100, NDV: 10}, 1},
		{"all NULL", ColStats{Rows: 500, NDV: 0, NullFrac: 1}, ColStats{Rows: 500, NDV: 20}, 1},
		{"half rounds to even", ColStats{Rows: 5, NDV: 2}, ColStats{Rows: 5, NDV: 2}, 12}, // 12.5 → 12
		{"key/foreign key", ColStats{Rows: 100000, NDV: 1000}, ColStats{Rows: 1000, NDV: 1000}, 100000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateEquiJoinRows(tc.r, tc.s); got != tc.want {
				t.Fatalf("EstimateEquiJoinRows(%+v, %+v) = %v, want %v", tc.r, tc.s, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------- 8.14

func sel3(ab, bc, ac float64) [][]float64 {
	return [][]float64{
		{1, ab, ac},
		{ab, 1, bc},
		{ac, bc, 1},
	}
}

func TestP8_14_BestJoinOrder(t *testing.T) {
	tests := []struct {
		name      string
		card      []float64
		sel       [][]float64
		wantOrder []int
		wantCost  float64
	}{
		{"starts with the small selective pair", []float64{1000, 10, 50}, sel3(0.01, 0.1, 1), []int{1, 2, 0}, 550},
		{"single table", []float64{42}, [][]float64{{1}}, []int{0}, 0},
		{"two tables", []float64{5, 3}, [][]float64{{1, 0.5}, {0.5, 1}}, []int{0, 1}, 7.5},
		// all pairs tie (size 100 each, final 1000): lexicographically smallest wins.
		{"full tie → lexicographic", []float64{10, 10, 10}, sel3(1, 1, 1), []int{0, 1, 2}, 1100},
		// chain A-B-C-D: card 100,1000,10,1000; sel AB=.01, BC=.01, CD=.001; no other predicates.
		// AB=1000 BC=100 CD=10 → start with C,D (10); then +B: 10*1000*.01=100; then +A: 100*100*.01=100 → 210.
		{"chain query avoids cross products", []float64{100, 1000, 10, 1000},
			[][]float64{
				{1, 0.01, 1, 1},
				{0.01, 1, 0.01, 1},
				{1, 0.01, 1, 0.001},
				{1, 1, 0.001, 1},
			}, []int{2, 3, 1, 0}, 210},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			order, cost, err := BestJoinOrder(tc.card, tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(order, tc.wantOrder) || math.Abs(cost-tc.wantCost) > 1e-9*tc.wantCost {
				t.Fatalf("order, cost = %v, %v; want %v, %v", order, cost, tc.wantOrder, tc.wantCost)
			}
		})
	}
}

func TestP8_14_BestJoinOrder_BadInput(t *testing.T) {
	tests := []struct {
		name string
		card []float64
		sel  [][]float64
	}{
		{"no tables", nil, nil},
		{"sel has too few rows", []float64{1, 2}, [][]float64{{1, 1}}},
		{"sel row too short", []float64{1, 2}, [][]float64{{1, 1}, {1}}},
		{"17 tables", make([]float64, 17), make([][]float64, 17)},
	}
	for _, tc := range tests {
		if _, _, err := BestJoinOrder(tc.card, tc.sel); !errors.Is(err, ErrBadInput) {
			t.Fatalf("%s: err = %v, want ErrBadInput", tc.name, err)
		}
	}
}

// bruteForceOrder tries all n! orders with the same cost definition.
func bruteForceOrder(card []float64, sel [][]float64) ([]int, float64) {
	n := len(card)
	size := func(set []int) float64 {
		s := slices.Sorted(slices.Values(set))
		sz := 1.0
		for a, i := range s {
			sz *= card[i]
			for _, j := range s[a+1:] {
				sz *= sel[i][j]
			}
		}
		return sz
	}
	var best []int
	bestCost := math.Inf(1)
	perm := make([]int, n)
	used := make([]bool, n)
	var rec func(k int)
	rec = func(k int) {
		if k == n {
			c := 0.0
			for m := 2; m <= n; m++ {
				c += size(perm[:m])
			}
			eq := math.Abs(c-bestCost) <= 1e-9*max(math.Abs(c), math.Abs(bestCost))
			if best == nil || (!eq && c < bestCost) || (eq && slices.Compare(perm, best) < 0) {
				best, bestCost = slices.Clone(perm), c
			}
			return
		}
		for i := 0; i < n; i++ {
			if !used[i] {
				used[i], perm[k] = true, i
				rec(k + 1)
				used[i] = false
			}
		}
	}
	rec(0)
	return best, bestCost
}

func TestP8_14_BestJoinOrder_MatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 14))
	sels := []float64{1, 1, 0.5, 0.1, 0.01, 0.001}
	for trial := range 200 {
		n := 3 + trial%3
		card := make([]float64, n)
		for i := range card {
			card[i] = float64(1 + rng.IntN(10000))
		}
		sel := make([][]float64, n)
		for i := range sel {
			sel[i] = make([]float64, n)
			sel[i][i] = 1
		}
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				s := sels[rng.IntN(len(sels))]
				sel[i][j], sel[j][i] = s, s
			}
		}
		wantOrder, wantCost := bruteForceOrder(card, sel)
		order, cost, err := BestJoinOrder(card, sel)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(order, wantOrder) || math.Abs(cost-wantCost) > 1e-9*wantCost {
			t.Fatalf("trial %d card=%v sel=%v: got %v cost %v, want %v cost %v", trial, card, sel, order, cost, wantOrder, wantCost)
		}
	}
}
