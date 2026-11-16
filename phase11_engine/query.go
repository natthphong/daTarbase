package engine

import (
	"fmt"
	"strings"

	executor "github.com/natthphong/daTarbase/phase06_executor"
	sortagg "github.com/natthphong/daTarbase/phase07_sortagg"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// This file is PROVIDED: the query builder, plans and EXPLAIN rendering.

// Cond is one WHERE condition "Col Op Val". Op is one of
// "=", "<>", "<", "<=", ">", ">=".
type Cond struct {
	Col string
	Op  string
	Val types.Value
}

// Agg is one aggregate of Query.Aggregate.
type Agg struct {
	Func sortagg.AggFunc
	Col  string // "" for COUNT(*)
}

// Name is the output column name: "count(*)", "sum(salary)", ...
func (a Agg) Name() string {
	fn := [...]string{"count", "count", "sum", "avg", "min", "max"}[a.Func]
	if a.Func == sortagg.AggCountStar {
		return "count(*)"
	}
	return fn + "(" + a.Col + ")"
}

func CountStar() Agg       { return Agg{Func: sortagg.AggCountStar} }
func Count(col string) Agg { return Agg{Func: sortagg.AggCount, Col: col} }
func Sum(col string) Agg   { return Agg{Func: sortagg.AggSum, Col: col} }
func Avg(col string) Agg   { return Agg{Func: sortagg.AggAvg, Col: col} }
func Min(col string) Agg   { return Agg{Func: sortagg.AggMin, Col: col} }
func Max(col string) Agg   { return Agg{Func: sortagg.AggMax, Col: col} }

// OrderItem is one ORDER BY key (ASC → NULLS LAST, DESC → NULLS FIRST).
type OrderItem struct {
	Col  string
	Desc bool
}

type joinClause struct {
	right             *Query
	typ               JoinType
	leftCol, rightCol string
}

// Query is a SELECT built with method chaining:
//
//	Scan("emp").Where("salary", ">", 100).
//	    Join(Scan("dept"), LeftJoin, "dept", "id").
//	    GroupBy("dept.name").Aggregate(CountStar(), Avg("salary")).
//	    OrderByDesc("count(*)").Limit(3)
//
// Evaluation order: scan of the base table (with its Where conditions) →
// joins, in call order (each right side is a Scan with its own Where) →
// GroupBy / Aggregate → OrderBy → Offset / Limit → Select.
//
// Column names: a scan of table t produces columns "t.col". Anywhere a column
// is named, "col" works when exactly one column ends in ".col"; otherwise
// write "t.col". After GroupBy the columns are the group columns (full names)
// followed by the aggregates' Name()s.
type Query struct {
	table      string
	conds      []Cond
	joins      []joinClause
	groupBy    []string
	aggs       []Agg
	orderBy    []OrderItem
	limit      int // -1 = no LIMIT
	offset     int
	selectCols []string
}

// Scan starts a query over a table.
func Scan(table string) *Query { return &Query{table: table, limit: -1} }

// Where adds "col op val" (ANDed) on the query's BASE table, whatever the call order.
// val is a Go literal (see toValue) or a types.Value.
func (q *Query) Where(col, op string, val any) *Query {
	q.conds = append(q.conds, Cond{Col: col, Op: op, Val: toValue(val)})
	return q
}

// Join joins the current result (left) with right ON left.leftCol = right.rightCol.
// right must be a plain Scan(...) with optional Where conditions.
func (q *Query) Join(right *Query, typ JoinType, leftCol, rightCol string) *Query {
	q.joins = append(q.joins, joinClause{right: right, typ: typ, leftCol: leftCol, rightCol: rightCol})
	return q
}

// GroupBy sets the GROUP BY columns.
func (q *Query) GroupBy(cols ...string) *Query { q.groupBy = append(q.groupBy, cols...); return q }

// Aggregate adds aggregates (with or without GroupBy).
func (q *Query) Aggregate(aggs ...Agg) *Query { q.aggs = append(q.aggs, aggs...); return q }

// OrderBy adds ascending sort keys.
func (q *Query) OrderBy(cols ...string) *Query {
	for _, c := range cols {
		q.orderBy = append(q.orderBy, OrderItem{Col: c})
	}
	return q
}

// OrderByDesc adds a descending sort key.
func (q *Query) OrderByDesc(col string) *Query {
	q.orderBy = append(q.orderBy, OrderItem{Col: col, Desc: true})
	return q
}

// Limit sets LIMIT n (n >= 0).
func (q *Query) Limit(n int) *Query { q.limit = n; return q }

// Offset sets OFFSET n.
func (q *Query) Offset(n int) *Query { q.offset = n; return q }

// Select sets the output columns (default: all).
func (q *Query) Select(cols ...string) *Query { q.selectCols = append(q.selectCols, cols...); return q }

// isPlainScan: only Scan + Where (what UPDATE / DELETE and join inputs accept).
func (q *Query) isPlainScan() bool {
	return len(q.joins) == 0 && len(q.groupBy) == 0 && len(q.aggs) == 0 && len(q.orderBy) == 0 &&
		q.limit < 0 && q.offset == 0 && len(q.selectCols) == 0
}

// neededColumns is the set of base-table columns a single-table query reads,
// or nil for "all of them" (joins, or SELECT * without grouping). It lets the
// planner pick an Index Only Scan when an index covers them.
func (q *Query) neededColumns() []string {
	if len(q.joins) > 0 {
		return nil
	}
	grouping := len(q.groupBy) > 0 || len(q.aggs) > 0
	if !grouping && len(q.selectCols) == 0 {
		return nil
	}
	need := []string{}
	if !grouping {
		need = append(need, q.selectCols...)
		for _, o := range q.orderBy {
			need = append(need, o.Col)
		}
	}
	need = append(need, q.groupBy...)
	for _, a := range q.aggs {
		if a.Func != sortagg.AggCountStar {
			need = append(need, a.Col)
		}
	}
	return need
}

// ---------------------------------------------------------------- names

// qualifiedNames lists a table's columns as "table.col".
func qualifiedNames(def *TableDef) []string {
	out := make([]string, len(def.Schema.Columns))
	for i, c := range def.Schema.Columns {
		out[i] = def.Name + "." + c.Name
	}
	return out
}

// resolve finds a column by name in an output column list: an exact match
// first, otherwise the unique column ending in "."+name.
// Errors wrap ErrNoSuchColumn or ErrAmbiguousColumn.
func resolve(cols []string, name string) (int, error) {
	found := -1
	for i, c := range cols {
		if c == name {
			if found >= 0 {
				return -1, fmt.Errorf("%w: %q", ErrAmbiguousColumn, name)
			}
			found = i
		}
	}
	if found >= 0 {
		return found, nil
	}
	for i, c := range cols {
		if strings.HasSuffix(c, "."+name) {
			if found >= 0 {
				return -1, fmt.Errorf("%w: %q", ErrAmbiguousColumn, name)
			}
			found = i
		}
	}
	if found < 0 {
		return -1, fmt.Errorf("%w: %q", ErrNoSuchColumn, name)
	}
	return found, nil
}

// boundCond is a Cond resolved against a table: column position, full
// column name, operator, and a value coerced to the column's kind.
type boundCond struct {
	Col  int
	Name string
	Op   executor.CmpOp
	Val  types.Value
}

var cmpOps = map[string]executor.CmpOp{
	"=": executor.OpEQ, "<>": executor.OpNE, "!=": executor.OpNE,
	"<": executor.OpLT, "<=": executor.OpLE, ">": executor.OpGT, ">=": executor.OpGE,
}

// bindConds resolves conditions against a table (ErrNoSuchColumn,
// ErrBadOperator, coercion errors).
func bindConds(def *TableDef, conds []Cond) ([]boundCond, error) {
	cols := qualifiedNames(def)
	out := make([]boundCond, 0, len(conds))
	for _, c := range conds {
		i, err := resolve(cols, c.Col)
		if err != nil {
			return nil, err
		}
		op, ok := cmpOps[c.Op]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrBadOperator, c.Op)
		}
		v, err := coerceValue(def.Schema.Columns[i].Kind, c.Val)
		if err != nil {
			return nil, err
		}
		out = append(out, boundCond{Col: i, Name: cols[i], Op: op, Val: v})
	}
	return out, nil
}

// predicate builds the phase-6 expression c1 AND c2 AND ... (left-deep) over
// rows whose columns are named cols. nil for no conditions.
func predicate(conds []boundCond, cols []string) (executor.Expr, error) {
	var e executor.Expr
	for _, c := range conds {
		i, err := resolve(cols, c.Name)
		if err != nil {
			return nil, err
		}
		cmp := executor.Cmp(c.Op, executor.Col(i, c.Name), executor.C(c.Val))
		if e == nil {
			e = cmp
		} else {
			e = executor.And{L: e, R: cmp}
		}
	}
	return e, nil
}

// ---------------------------------------------------------------- plans

// Plan is a planned (not yet opened) query: the iterator tree to run, the
// names of its output columns, and the node tree EXPLAIN prints.
type Plan struct {
	Iter exec.Iterator
	Cols []string
	Node *PlanNode
}

// PlanNode is one line of EXPLAIN (COSTS OFF) output plus its detail lines.
type PlanNode struct {
	Name     string   // e.g. "Seq Scan on emp", "Hash Left Join", "Limit"
	Details  []string // e.g. "Filter: (emp.salary > 100)"
	Children []*PlanNode
}

// String renders the tree like Postgres' EXPLAIN (COSTS OFF):
//
//	Limit
//	  ->  Sort
//	        Sort Key: emp.name
//	        ->  Seq Scan on emp
//	              Filter: (emp.dept = 10)
//
// Header at depth d ≥ 1: 6(d-1)+2 spaces then "->  "; detail lines: 2 spaces at
// depth 0, 6d+2 spaces below.
func (n *PlanNode) String() string {
	var lines []string
	var walk func(n *PlanNode, d int)
	walk = func(n *PlanNode, d int) {
		h, detail := n.Name, "  "
		if d > 0 {
			h = strings.Repeat(" ", 6*(d-1)+2) + "->  " + h
			detail = strings.Repeat(" ", 6*d+2)
		}
		lines = append(lines, h)
		for _, l := range n.Details {
			lines = append(lines, detail+l)
		}
		for _, c := range n.Children {
			walk(c, d+1)
		}
	}
	if n != nil {
		walk(n, 0)
	}
	return strings.Join(lines, "\n")
}

// Explain plans q (in a throw-away read-only transaction) and returns its
// EXPLAIN (COSTS OFF) text, or "ERROR: ..." if it cannot be planned.
func (db *DB) Explain(q *Query) string {
	t := db.Begin(ReadCommitted)
	defer t.finish()
	p, err := t.PlanQuery(q)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	return p.Node.String()
}

// materialized is an iterator whose rows are computed all at once by run when
// it is opened (used around phase-7/8 functions that return []types.Row).
type materialized struct {
	run  func() ([]types.Row, error)
	rows []types.Row
	pos  int
	open bool
}

func (m *materialized) Open() error {
	rows, err := m.run()
	if err != nil {
		return err
	}
	m.rows, m.pos, m.open = rows, 0, true
	return nil
}

func (m *materialized) Next() (types.Row, bool, error) {
	if !m.open {
		return nil, false, exec.ErrNotOpen
	}
	if m.pos >= len(m.rows) {
		return nil, false, nil
	}
	m.pos++
	return m.rows[m.pos-1], true, nil
}

func (m *materialized) Close() error { m.open, m.rows = false, nil; return nil }
