package executor

import (
	"fmt"
	"strings"
)

// Plan is one node of a plan tree as EXPLAIN shows it (Postgres: Plan + the
// bits ExplainNode prints).
type Plan struct {
	Node     string // node name: "Seq Scan", "Index Scan", "Index Only Scan", "Bitmap Heap Scan", "Bitmap Index Scan", "BitmapAnd", "Limit", ...
	Index    string // index used, "" if none
	Relation string // table scanned, "" if none

	StartupCost float64 // cost before the first row
	TotalCost   float64 // cost to return all rows
	Rows        int64   // estimated output rows
	Width       int     // estimated average row width in bytes

	IndexCond   Expr // printed as "Index Cond: ..." when non-nil
	RecheckCond Expr // printed as "Recheck Cond: ..." when non-nil
	Filter      Expr // printed as "Filter: ..." when non-nil

	Children []*Plan // input plans, printed in order
}

// Problem 6.15 — Explain [Easy]
//
// Render a plan tree as indented text in the format of Postgres' EXPLAIN.
//
// Rules:
//  1. One header line per node:
//     <Node>[ using <Index> on <Relation>]  (cost=<startup>..<total> rows=<Rows> width=<Width>)
//     - Index and Relation both set → " using <Index> on <Relation>"
//     - only Index → " on <Index>"   (how "Bitmap Index Scan on t_a_idx" reads)
//     - only Relation → " on <Relation>";  neither → nothing.
//     - TWO spaces before "(cost=". Costs use exactly 2 decimals (%.2f).
//  2. Then the node's detail lines, in this order, only for non-nil fields:
//     "Index Cond: " + IndexCond.String(), "Recheck Cond: " + RecheckCond.String(),
//     "Filter: " + Filter.String().
//  3. Then each child, recursively (depth-first, in order).
//  4. Indentation, for a node at depth d (root d = 0):
//     - header: d = 0 → no indent; d ≥ 1 → (6·(d-1) + 2) spaces, then "->  " (arrow + 2 spaces)
//     - detail lines: d = 0 → 2 spaces; d ≥ 1 → (6·d + 2) spaces
//  5. Lines are joined with "\n"; no trailing newline. A nil plan → "".
//
// Example:
//
//	Limit  (cost=0.29..0.83 rows=10 width=8)
//	  ->  Bitmap Heap Scan on t  (cost=4.30..12.50 rows=10 width=8)
//	        Recheck Cond: ((a = 1) OR (b = 2))
//	        Filter: (c > 0)
//	        ->  BitmapOr  (cost=4.30..4.30 rows=10 width=0)
//	              ->  Bitmap Index Scan on t_a_idx  (cost=0.00..2.15 rows=5 width=0)
//	                    Index Cond: (a = 1)
//
// Constraints: O(nodes).
// Postgres reference: ExplainNode() in src/backend/commands/explain.c
func Explain(p *Plan) string {
	var lines []string
	var walk func(n *Plan, d int)
	walk = func(n *Plan, d int) {
		h := n.Node
		switch {
		case n.Index != "" && n.Relation != "":
			h += " using " + n.Index + " on " + n.Relation
		case n.Index != "":
			h += " on " + n.Index
		case n.Relation != "":
			h += " on " + n.Relation
		}
		h += fmt.Sprintf("  (cost=%.2f..%.2f rows=%d width=%d)", n.StartupCost, n.TotalCost, n.Rows, n.Width)
		detail := "  "
		if d > 0 {
			h = strings.Repeat(" ", 6*(d-1)+2) + "->  " + h
			detail = strings.Repeat(" ", 6*d+2)
		}
		lines = append(lines, h)
		if n.IndexCond != nil {
			lines = append(lines, detail+"Index Cond: "+n.IndexCond.String())
		}
		if n.RecheckCond != nil {
			lines = append(lines, detail+"Recheck Cond: "+n.RecheckCond.String())
		}
		if n.Filter != nil {
			lines = append(lines, detail+"Filter: "+n.Filter.String())
		}
		for _, c := range n.Children {
			walk(c, d+1)
		}
	}
	if p == nil {
		return ""
	}
	walk(p, 0)
	return strings.Join(lines, "\n")
}
