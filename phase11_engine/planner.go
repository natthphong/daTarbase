package engine

import (
	txn "github.com/natthphong/daTarbase/phase09_txn"
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
	panic("TODO: Problem 11.14")
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
	panic("TODO: Problem 11.15")
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
	panic("TODO: Problem 11.16")
}
