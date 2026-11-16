package executor

import (
	"strings"
	"testing"
)

func TestP6_15_Explain(t *testing.T) {
	a, b, c := Col(0, "a"), Col(1, "b"), Col(2, "c")
	plan := &Plan{
		Node: "Limit", StartupCost: 0.29, TotalCost: 0.83, Rows: 10, Width: 8,
		Children: []*Plan{{
			Node: "Bitmap Heap Scan", Relation: "t", StartupCost: 4.3, TotalCost: 12.5, Rows: 10, Width: 8,
			RecheckCond: Or{L: Cmp(OpEQ, a, k(1)), R: Cmp(OpEQ, b, k(2))},
			Filter:      Cmp(OpGT, c, k(0)),
			Children: []*Plan{{
				Node: "BitmapOr", StartupCost: 4.3, TotalCost: 4.3, Rows: 10, Width: 0,
				Children: []*Plan{
					{Node: "Bitmap Index Scan", Index: "t_a_idx", TotalCost: 2.15, Rows: 5, IndexCond: Cmp(OpEQ, a, k(1))},
					{Node: "Bitmap Index Scan", Index: "t_b_idx", TotalCost: 2.15, Rows: 5, IndexCond: Cmp(OpEQ, b, k(2))},
				},
			}},
		}},
	}
	want := strings.Join([]string{
		"Limit  (cost=0.29..0.83 rows=10 width=8)",
		"  ->  Bitmap Heap Scan on t  (cost=4.30..12.50 rows=10 width=8)",
		"        Recheck Cond: ((a = 1) OR (b = 2))",
		"        Filter: (c > 0)",
		"        ->  BitmapOr  (cost=4.30..4.30 rows=10 width=0)",
		"              ->  Bitmap Index Scan on t_a_idx  (cost=0.00..2.15 rows=5 width=0)",
		"                    Index Cond: (a = 1)",
		"              ->  Bitmap Index Scan on t_b_idx  (cost=0.00..2.15 rows=5 width=0)",
		"                    Index Cond: (b = 2)",
	}, "\n")
	if got := Explain(plan); got != want {
		t.Fatalf("Explain mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestP6_15_Explain_Cases(t *testing.T) {
	id := Col(0, "id")
	tests := []struct {
		name string
		plan *Plan
		want []string
	}{
		{"single seq scan with filter (root details indent 2)",
			&Plan{Node: "Seq Scan", Relation: "users", TotalCost: 2250, Rows: 40000, Width: 36, Filter: Cmp(OpGT, id, k(60000))},
			[]string{
				"Seq Scan on users  (cost=0.00..2250.00 rows=40000 width=36)",
				"  Filter: (id > 60000)",
			}},
		{"index scan: using ... on ..., Index Cond before Filter",
			&Plan{Node: "Index Scan", Index: "users_pkey", Relation: "users", StartupCost: 0.29, TotalCost: 8.306, Rows: 1, Width: 36,
				Filter: IsNotNull{X: Col(1, "email")}, IndexCond: Cmp(OpEQ, id, k(42))},
			[]string{
				"Index Scan using users_pkey on users  (cost=0.29..8.31 rows=1 width=36)",
				"  Index Cond: (id = 42)",
				"  Filter: (email IS NOT NULL)",
			}},
		{"node without index or relation",
			&Plan{Node: "Result", TotalCost: 0.01, Rows: 1, Width: 4},
			[]string{"Result  (cost=0.00..0.01 rows=1 width=4)"}},
		{"two-level chain without details",
			&Plan{Node: "Limit", TotalCost: 1, Rows: 1, Width: 4, Children: []*Plan{
				{Node: "Index Only Scan", Index: "t_idx", Relation: "t", TotalCost: 1, Rows: 100, Width: 4, Children: nil},
			}},
			[]string{
				"Limit  (cost=0.00..1.00 rows=1 width=4)",
				"  ->  Index Only Scan using t_idx on t  (cost=0.00..1.00 rows=100 width=4)",
			}},
		{"siblings at the same depth, then back up",
			&Plan{Node: "Append", TotalCost: 3, Rows: 3, Width: 4, Children: []*Plan{
				{Node: "Limit", TotalCost: 1, Rows: 1, Width: 4, Children: []*Plan{
					{Node: "Seq Scan", Relation: "a", TotalCost: 1, Rows: 1, Width: 4, Filter: Cmp(OpLT, Col(0, "x"), k(1))},
				}},
				{Node: "Seq Scan", Relation: "b", TotalCost: 2, Rows: 2, Width: 4},
			}},
			[]string{
				"Append  (cost=0.00..3.00 rows=3 width=4)",
				"  ->  Limit  (cost=0.00..1.00 rows=1 width=4)",
				"        ->  Seq Scan on a  (cost=0.00..1.00 rows=1 width=4)",
				"              Filter: (x < 1)",
				"  ->  Seq Scan on b  (cost=0.00..2.00 rows=2 width=4)",
			}},
		{"unnamed columns and text constants",
			&Plan{Node: "Seq Scan", Relation: "t", TotalCost: 1, Rows: 1, Width: 4, Filter: Cmp(OpEQ, ColRef{Index: 1}, k("bob"))},
			[]string{
				"Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)",
				"  Filter: ($1 = 'bob')",
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := strings.Join(tc.want, "\n")
			if got := Explain(tc.plan); got != want {
				t.Fatalf("Explain mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
	if got := Explain(nil); got != "" {
		t.Fatalf("Explain(nil) = %q, want \"\"", got)
	}
}
