package engine

import (
	"fmt"
	"slices"
	"strings"

	executor "github.com/natthphong/daTarbase/phase06_executor"
	sortagg "github.com/natthphong/daTarbase/phase07_sortagg"
	join "github.com/natthphong/daTarbase/phase08_join"
	txn "github.com/natthphong/daTarbase/phase09_txn"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// ScanRequest describes the scan of one table inside a statement.
type ScanRequest struct {
	Table   string
	Conds   []Cond   // WHERE conditions on this table, ANDed
	Need    []string // columns the query reads (nil = all); enables Index Only Scans
	EmitTID bool     // append a "ctid" column (UPDATE / DELETE need it)
}

// Problem 11.14 — Txn.PlanScan [Hard]
//
// Choose the access path for one table — Seq Scan, Index Scan or Index Only Scan — and put the remaining conditions in a Filter.
//
// The planner turns "which rows of this table?" into an iterator. With an
// index whose LEADING column has a usable condition, it can visit only the
// matching key range instead of every page:
//
//	Seq Scan on emp                   Index Scan using emp_dept on emp     Index Only Scan using emp_dept on emp
//	  Filter: (emp.dept = 10)           Index Cond: (emp.dept = 10)          Index Cond: (emp.dept = 10)
//
// Rules:
//  1. Unknown table → wraps ErrNoSuchTable. conds := bindConds(def, req.Conds)
//     (provided; resolves names to "table.col", coerces values) — errors as-is.
//  2. Usable condition: op is =, <, <=, > or >= (not <>), the value is not
//     NULL, and some index of the table (t.db.indexesOf, catalog order) has the
//     condition's column as its FIRST key column — take the first such index.
//     Pick the first usable condition with op "=" (in Where order); if there is
//     none, the first usable range condition. No usable condition → Seq Scan.
//  3. Columns: Seq Scan and Index Scan output qualifiedNames(def); an Index
//     Only Scan outputs ix.Columns(). If req.EmitTID, append "ctid" (and the
//     scan's EmitTID is set).
//  4. Index Only Scan instead of Index Scan when ALL hold: !req.EmitTID,
//     req.Need != nil, and ix.covers() every column named in req.Need (resolve
//     each over qualifiedNames(def); errors as-is) and every condition column.
//  5. Scans get Snap = snap, Clog = t.db.mgr.Clog, Mine = t.mine(); index scans
//     get Lo, Hi from KeyBounds(cond.Op, cond.Val) (Problem 11.11).
//  6. Every condition except the one used by the index goes into a Filter
//     (phase 6): pred := predicate(rest, cols) (provided, ANDed in Where order),
//     iterator &executor.Filter{Child: scan, Pred: pred}.
//  7. The plan node (one node: Postgres prints the filter on the scan itself):
//     Name "Seq Scan on <table>" | "Index Scan using <index> on <table>" |
//     "Index Only Scan using <index> on <table>"; Details, in this order:
//     "Index Cond: " + predicate([cond], cols).String() (index scans only),
//     then "Filter: " + pred.String() if there is a filter.
//
// Example (emp(id, name, dept, salary); indexes emp_dept (dept) INCLUDE (name),
// then emp_id (id)):
//
//	Where dept = 10                       → Index Scan using emp_dept on emp; Index Cond: (emp.dept = 10)
//	Where salary > 100, id >= 40          → Index Scan using emp_id; Index Cond: (emp.id >= 40);
//	                                        Filter: (emp.salary > 100)
//	Where id > 5, dept = 10               → emp_dept wins (equality beats range); Filter: (emp.id > 5)
//	Where dept <> 10 / dept = NULL        → Seq Scan on emp; Filter: (emp.dept <> 10) / (emp.dept = NULL)
//	Where dept = 10, Need [name]          → Index Only Scan using emp_dept on emp, cols [emp.dept emp.name]
//	Where name = 'x'                      → Seq Scan (no index leads with name)
//
// Constraints: no I/O while planning (iterators are opened by the caller).
// Postgres reference: create_index_paths() / match_clause_to_indexcol() in
// src/backend/optimizer/path/indxpath.c, create_scan_plan() in createplan.c
func (t *Txn) PlanScan(snap *txn.Snapshot, req ScanRequest) (*Plan, error) {
	h := t.db.heaps[req.Table]
	if h == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoSuchTable, req.Table)
	}
	def := h.Def
	conds, err := bindConds(def, req.Conds)
	if err != nil {
		return nil, err
	}
	var best *Index
	bestCond := -1
	for i, c := range conds {
		if c.Op == executor.OpNE || c.Val.IsNull() {
			continue
		}
		for _, ix := range t.db.indexesOf(def.Name) {
			if ix.KeyCols[0] != c.Col {
				continue
			}
			if best == nil || (c.Op == executor.OpEQ && conds[bestCond].Op != executor.OpEQ) {
				best, bestCond = ix, i
			}
			break
		}
	}
	cols := qualifiedNames(def)
	clog, mine := t.db.mgr.Clog, t.mine()
	node := &PlanNode{}
	var it exec.Iterator
	if best == nil {
		it = &SeqScan{Heap: h, Snap: snap, Clog: clog, Mine: mine, EmitTID: req.EmitTID}
		node.Name = "Seq Scan on " + def.Name
	} else {
		c := conds[bestCond]
		lo, hi, err := KeyBounds(c.Op, c.Val)
		if err != nil {
			return nil, err
		}
		covering := !req.EmitTID && req.Need != nil
		if covering {
			var used []int
			for _, n := range req.Need {
				i, err := resolve(cols, n)
				if err != nil {
					return nil, err
				}
				used = append(used, i)
			}
			for _, bc := range conds {
				used = append(used, bc.Col)
			}
			covering = best.covers(used)
		}
		if covering {
			it = &IndexOnlyScan{Index: best, Lo: lo, Hi: hi, Snap: snap, Clog: clog, Mine: mine}
			cols = best.Columns()
			node.Name = "Index Only Scan using " + best.Def.Name + " on " + def.Name
		} else {
			it = &IndexScan{Index: best, Lo: lo, Hi: hi, Snap: snap, Clog: clog, Mine: mine, EmitTID: req.EmitTID}
			node.Name = "Index Scan using " + best.Def.Name + " on " + def.Name
		}
		ic, err := predicate([]boundCond{c}, cols)
		if err != nil {
			return nil, err
		}
		node.Details = append(node.Details, "Index Cond: "+ic.String())
		conds = slices.Delete(slices.Clone(conds), bestCond, bestCond+1)
	}
	if req.EmitTID {
		cols = append(cols, "ctid")
	}
	if len(conds) > 0 {
		pred, err := predicate(conds, cols)
		if err != nil {
			return nil, err
		}
		it = &executor.Filter{Child: it, Pred: pred}
		node.Details = append(node.Details, "Filter: "+pred.String())
	}
	return &Plan{Iter: it, Cols: cols, Node: node}, nil
}

// Problem 11.15 — PlanJoin [Medium]
//
// Join two plans ON left.leftCol = right.rightCol, choosing the phase-8 algorithm by join type.
//
// Every engine join is an equi-join, so INNER / LEFT / RIGHT / FULL use a
// hash join (phase 8): the RIGHT input is the build side (Postgres' "Hash"
// node), the left input probes it. SEMI / ANTI use phase 8's nested loop,
// which stops each inner scan at the first match.
//
//	Hash Left Join                          Nested Loop Anti Join
//	  Hash Cond: (emp.dept = dept.id)         Join Filter: (emp.dept = dept.id)
//	  ->  Seq Scan on emp                     ->  Seq Scan on emp
//	  ->  Hash                                ->  Seq Scan on dept
//	        ->  Seq Scan on dept
//
// Rules:
//  1. l := resolve(left.Cols, leftCol), r := resolve(right.Cols, rightCol);
//     errors as-is.
//  2. spec := join.JoinSpec{Type: typ, Keys: [{Left: l, Right: r}],
//     LeftWidth: len(left.Cols), RightWidth: len(right.Cols)}.
//  3. The iterator is a provided materialized{run: ...} that, when opened,
//     calls the phase-8 function on (left.Iter, right.Iter, spec):
//     Inner → join.HashJoinInner; Left, Right, Full → join.HashJoinOuter;
//     Semi, Anti → join.NestedLoopSemiAnti. Any other type → wraps ErrBadQuery.
//  4. Output columns: left.Cols ++ right.Cols; Semi/Anti: left.Cols only.
//  5. cond := "(" + left.Cols[l] + " = " + right.Cols[r] + ")". Nodes:
//     Inner "Hash Join", Left "Hash Left Join", Right "Hash Right Join",
//     Full "Hash Full Join" — Details ["Hash Cond: " + cond], Children
//     [left.Node, {Name: "Hash", Children: [right.Node]}];
//     Semi "Nested Loop Semi Join", Anti "Nested Loop Anti Join" — Details
//     ["Join Filter: " + cond], Children [left.Node, right.Node].
//
// Example (emp(id,name,dept,salary) ⨝ dept(id,name)):
//
//	PlanJoin(emp, dept, LeftJoin, "dept", "dept.id") → cols [emp.id emp.name emp.dept emp.salary dept.id dept.name]
//	PlanJoin(emp, dept, LeftJoin, "dept", "id")      → fine: each name is resolved on its own side
//	joining that result again ON "id" = …            → ErrAmbiguousColumn (emp.id and dept.id)
//	an employee with dept NULL → (…, NULL, NULL) in a LEFT join, missing in an INNER join;
//	department 50 without employees → (NULL, NULL, NULL, NULL, 50, 'legal') in RIGHT / FULL
//
// Constraints: each input is opened once per execution.
// Postgres reference: hash_inner_and_outer() in src/backend/optimizer/path/joinpath.c,
// ExecHashJoin() in src/backend/executor/nodeHashjoin.c
func PlanJoin(left, right *Plan, typ JoinType, leftCol, rightCol string) (*Plan, error) {
	l, err := resolve(left.Cols, leftCol)
	if err != nil {
		return nil, err
	}
	r, err := resolve(right.Cols, rightCol)
	if err != nil {
		return nil, err
	}
	spec := join.JoinSpec{Type: typ, Keys: []join.KeyPair{{Left: l, Right: r}}, LeftWidth: len(left.Cols), RightWidth: len(right.Cols)}
	cond := "(" + left.Cols[l] + " = " + right.Cols[r] + ")"
	var fn func(a, b exec.Iterator, s join.JoinSpec) ([]types.Row, error)
	node := &PlanNode{}
	cols := append(slices.Clone(left.Cols), right.Cols...)
	switch typ {
	case join.Inner, join.Left, join.Right, join.Full:
		fn = join.HashJoinOuter
		if typ == join.Inner {
			fn = join.HashJoinInner
		}
		node.Name = map[JoinType]string{join.Inner: "Hash Join", join.Left: "Hash Left Join", join.Right: "Hash Right Join", join.Full: "Hash Full Join"}[typ]
		node.Details = []string{"Hash Cond: " + cond}
		node.Children = []*PlanNode{left.Node, {Name: "Hash", Children: []*PlanNode{right.Node}}}
	case join.Semi, join.Anti:
		fn = join.NestedLoopSemiAnti
		node.Name = "Nested Loop Semi Join"
		if typ == join.Anti {
			node.Name = "Nested Loop Anti Join"
		}
		node.Details = []string{"Join Filter: " + cond}
		node.Children = []*PlanNode{left.Node, right.Node}
		cols = slices.Clone(left.Cols)
	default:
		return nil, fmt.Errorf("%w: join type %s", ErrBadQuery, typ)
	}
	li, ri := left.Iter, right.Iter
	it := &materialized{run: func() ([]types.Row, error) { return fn(li, ri, spec) }}
	return &Plan{Iter: it, Cols: cols, Node: node}, nil
}

// Problem 11.16 — Txn.PlanQuery [Hard]
//
// Plan a whole SELECT: base scan, joins, aggregation, sort, limit and projection, stacked into one iterator tree.
//
// Rules (one statement = one snapshot):
//  1. t.done → ErrTxnClosed. snap := t.Snapshot() (Problem 11.3), used by every scan.
//  2. Base: t.PlanScan(snap, ScanRequest{Table: q.table, Conds: q.conds,
//     Need: q.neededColumns()}).
//  3. Joins in order: the right side must be a plain scan (j.right.isPlainScan(),
//     else wraps ErrBadQuery): t.PlanScan(snap, ScanRequest{Table: j.right.table,
//     Conds: j.right.conds}), then PlanJoin(current, right, j.typ, j.leftCol, j.rightCol).
//  4. If q.groupBy or q.aggs is non-empty: resolve the group columns and every
//     aggregate column (not for COUNT(*)) over the current columns; the iterator
//     is a materialized running sortagg.HashAggregate(child, groupCols,
//     []sortagg.AggSpec{{Func, Col}…}, nil). Output columns: the group columns'
//     full names, then each Agg.Name(). Node "HashAggregate" with detail
//     "Group Key: " + full group names joined by ", "; without GROUP BY the
//     node is "Aggregate" with no detail.
//  5. If q.orderBy is non-empty: keys := sortagg.Key(col, desc) for each item
//     (Postgres' NULL placement). With a limit (q.limit >= 0) use
//     sortagg.NewTopN(child, q.limit+q.offset, keys...), else sortagg.NewSort.
//     Node "Sort", details "Sort Key: " + items joined by ", " (full name, plus
//     " DESC" for descending), and for TopN a second detail
//     "Sort Method: top-N heapsort".
//  6. If q.limit >= 0 or q.offset > 0: &executor.Limit{Child, Count: q.limit,
//     Offset: q.offset}, node "Limit".
//  7. If q.selectCols is non-empty: &executor.Project with one
//     executor.Col(i, name) per selected column (resolved over the current
//     columns); output columns = their full names. No plan node (Postgres
//     shows the target list only in EXPLAIN VERBOSE).
//  8. Any resolve error is returned as-is. Each new node has the previous one
//     as its only child.
//
// Example (indexes emp_dept (dept) INCLUDE (name) and emp_id (id)):
//
//	Scan("emp").Where("dept", "=", 10).OrderBy("name").Limit(2).Select("name")
//	  Limit
//	    ->  Sort
//	          Sort Key: emp.name
//	          Sort Method: top-N heapsort
//	          ->  Index Only Scan using emp_dept on emp      (only dept and name are needed)
//	                Index Cond: (emp.dept = 10)
//	  cols [emp.name]
//
//	Scan("emp").GroupBy("dept").Aggregate(CountStar(), Sum("salary")).OrderBy("dept")
//	  cols [emp.dept count(*) sum(salary)]
//
// Constraints: nothing is opened or read while planning.
// Postgres reference: grouping_planner() in src/backend/optimizer/plan/planner.c
func (t *Txn) PlanQuery(q *Query) (*Plan, error) {
	if t.done {
		return nil, ErrTxnClosed
	}
	snap, err := t.Snapshot()
	if err != nil {
		return nil, err
	}
	p, err := t.PlanScan(snap, ScanRequest{Table: q.table, Conds: q.conds, Need: q.neededColumns()})
	if err != nil {
		return nil, err
	}
	for _, j := range q.joins {
		if !j.right.isPlainScan() {
			return nil, fmt.Errorf("%w: the right side of a join must be Scan(table).Where(...)", ErrBadQuery)
		}
		r, err := t.PlanScan(snap, ScanRequest{Table: j.right.table, Conds: j.right.conds})
		if err != nil {
			return nil, err
		}
		if p, err = PlanJoin(p, r, j.typ, j.leftCol, j.rightCol); err != nil {
			return nil, err
		}
	}
	if len(q.groupBy) > 0 || len(q.aggs) > 0 {
		var groupCols []int
		var cols []string
		for _, g := range q.groupBy {
			i, err := resolve(p.Cols, g)
			if err != nil {
				return nil, err
			}
			groupCols = append(groupCols, i)
			cols = append(cols, p.Cols[i])
		}
		var specs []sortagg.AggSpec
		for _, a := range q.aggs {
			spec := sortagg.AggSpec{Func: a.Func}
			if a.Func != sortagg.AggCountStar {
				i, err := resolve(p.Cols, a.Col)
				if err != nil {
					return nil, err
				}
				spec.Col = i
			}
			specs = append(specs, spec)
			cols = append(cols, a.Name())
		}
		node := &PlanNode{Name: "Aggregate", Children: []*PlanNode{p.Node}}
		if len(groupCols) > 0 {
			node.Name = "HashAggregate"
			node.Details = []string{"Group Key: " + strings.Join(cols[:len(groupCols)], ", ")}
		}
		child := p.Iter
		it := &materialized{run: func() ([]types.Row, error) { return sortagg.HashAggregate(child, groupCols, specs, nil) }}
		p = &Plan{Iter: it, Cols: cols, Node: node}
	}
	if len(q.orderBy) > 0 {
		var keys []sortagg.SortKey
		var names []string
		for _, o := range q.orderBy {
			i, err := resolve(p.Cols, o.Col)
			if err != nil {
				return nil, err
			}
			keys = append(keys, sortagg.Key(i, o.Desc))
			n := p.Cols[i]
			if o.Desc {
				n += " DESC"
			}
			names = append(names, n)
		}
		node := &PlanNode{Name: "Sort", Details: []string{"Sort Key: " + strings.Join(names, ", ")}, Children: []*PlanNode{p.Node}}
		var it exec.Iterator
		if q.limit >= 0 {
			it = sortagg.NewTopN(p.Iter, q.limit+q.offset, keys...)
			node.Details = append(node.Details, "Sort Method: top-N heapsort")
		} else {
			it = sortagg.NewSort(p.Iter, keys...)
		}
		p = &Plan{Iter: it, Cols: p.Cols, Node: node}
	}
	if q.limit >= 0 || q.offset > 0 {
		p = &Plan{
			Iter: &executor.Limit{Child: p.Iter, Count: q.limit, Offset: q.offset},
			Cols: p.Cols,
			Node: &PlanNode{Name: "Limit", Children: []*PlanNode{p.Node}},
		}
	}
	if len(q.selectCols) > 0 {
		var exprs []executor.Expr
		var cols []string
		for _, s := range q.selectCols {
			i, err := resolve(p.Cols, s)
			if err != nil {
				return nil, err
			}
			exprs = append(exprs, executor.Col(i, p.Cols[i]))
			cols = append(cols, p.Cols[i])
		}
		p = &Plan{Iter: &executor.Project{Child: p.Iter, Exprs: exprs}, Cols: cols, Node: p.Node}
	}
	return p, nil
}
