package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	executor "github.com/natthphong/daTarbase/phase06_executor"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// company creates emp (50 rows) and dept (5 rows), committed, with indexes
// emp_dept (dept) INCLUDE (name) and emp_id (id).
func company(t testing.TB, opts Options) *DB {
	t.Helper()
	db := newDB(t, opts)
	mustCreate(t, db, "emp", empSchema())
	mustCreate(t, db, "dept", deptSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}, Include("name")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateIndex("emp_id", "emp", []string{"id"}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	for _, r := range emp50() {
		if _, err := tx.Insert("emp", r); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range dept5() {
		if _, err := tx.Insert("dept", r); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

func planScan(t testing.TB, db *DB, req ScanRequest) *Plan {
	t.Helper()
	tx := db.Begin(ReadCommitted)
	t.Cleanup(tx.finish)
	snap, _ := tx.Snapshot()
	p, err := tx.PlanScan(snap, req)
	if err != nil {
		t.Fatalf("PlanScan(%+v): %v", req, err)
	}
	return p
}

func conds(cs ...any) []Cond {
	var out []Cond
	for i := 0; i < len(cs); i += 3 {
		out = append(out, Cond{Col: cs[i].(string), Op: cs[i+1].(string), Val: toValue(cs[i+2])})
	}
	return out
}

func TestP11_14_PlanScan_ChoosesAccessPath(t *testing.T) {
	db := company(t, Options{})
	tests := []struct {
		name    string
		req     ScanRequest
		explain string
	}{
		{"equality on an indexed column", ScanRequest{Table: "emp", Conds: conds("dept", "=", 10)},
			"Index Scan using emp_dept on emp\n  Index Cond: (emp.dept = 10)"},
		{"range on the second index", ScanRequest{Table: "emp", Conds: conds("salary", ">", 100, "id", ">=", 40)},
			"Index Scan using emp_id on emp\n  Index Cond: (emp.id >= 40)\n  Filter: (emp.salary > 100)"},
		{"equality beats an earlier range", ScanRequest{Table: "emp", Conds: conds("id", ">", 5, "dept", "=", 10)},
			"Index Scan using emp_dept on emp\n  Index Cond: (emp.dept = 10)\n  Filter: (emp.id > 5)"},
		{"<> cannot use an index", ScanRequest{Table: "emp", Conds: conds("dept", "<>", 10)},
			"Seq Scan on emp\n  Filter: (emp.dept <> 10)"},
		{"= NULL cannot use an index", ScanRequest{Table: "emp", Conds: conds("dept", "=", nil)},
			"Seq Scan on emp\n  Filter: (emp.dept = NULL)"},
		{"no index leads with name", ScanRequest{Table: "emp", Conds: conds("name", "=", "x")},
			"Seq Scan on emp\n  Filter: (emp.name = 'x')"},
		{"no conditions", ScanRequest{Table: "dept"}, "Seq Scan on dept"},
		{"covered columns → index only", ScanRequest{Table: "emp", Conds: conds("dept", "=", 10), Need: []string{"name"}},
			"Index Only Scan using emp_dept on emp\n  Index Cond: (emp.dept = 10)"},
		{"covered filter column", ScanRequest{Table: "emp", Conds: conds("dept", ">=", 30, "name", "<", "m"), Need: []string{}},
			"Index Only Scan using emp_dept on emp\n  Index Cond: (emp.dept >= 30)\n  Filter: (emp.name < 'm')"},
		{"salary is not covered", ScanRequest{Table: "emp", Conds: conds("dept", "=", 10), Need: []string{"name", "salary"}},
			"Index Scan using emp_dept on emp\n  Index Cond: (emp.dept = 10)"},
		{"EmitTID never index only", ScanRequest{Table: "emp", Conds: conds("dept", "=", 10), Need: []string{"name"}, EmitTID: true},
			"Index Scan using emp_dept on emp\n  Index Cond: (emp.dept = 10)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := planScan(t, db, tc.req)
			if got := p.Node.String(); got != tc.explain {
				t.Fatalf("plan:\n%s\nwant:\n%s", got, tc.explain)
			}
		})
	}
}

func TestP11_14_PlanScan_ColumnsAndRows(t *testing.T) {
	db := company(t, Options{})
	p := planScan(t, db, ScanRequest{Table: "emp", Conds: conds("dept", "=", 10), Need: []string{"name"}})
	if !slices.Equal(p.Cols, []string{"emp.dept", "emp.name"}) {
		t.Fatalf("index-only cols = %v", p.Cols)
	}
	rows, err := exec.Collect(p.Iter)
	if err != nil {
		t.Fatal(err)
	}
	want := project(filterRows(emp50(), func(r types.Row) bool { return cmpOp(r[2], "=", types.Int32(10)) }), 2, 1)
	testkit.AssertRowsUnordered(t, rows, want)

	p = planScan(t, db, ScanRequest{Table: "emp", Conds: conds("emp.id", "<=", 2), EmitTID: true})
	if !slices.Equal(p.Cols, []string{"emp.id", "emp.name", "emp.dept", "emp.salary", "ctid"}) {
		t.Fatalf("cols = %v", p.Cols)
	}
	rows, _ = exec.Collect(p.Iter)
	testkit.AssertRows(t, rows, []types.Row{R(1, "alice", 20, 87, 1), R(2, "bob", 30, 124, 2)})
}

// Many condition lists: the plan's rows must equal a filter over the data.
func TestP11_14_PlanScan_MatchesOracle(t *testing.T) {
	db := company(t, Options{IndexFanout: 4})
	lists := [][]Cond{
		conds("dept", "=", 20),
		conds("dept", "<", 30),
		conds("dept", ">", 20, "salary", "<", 100),
		conds("id", ">=", 45),
		conds("id", "<", 10, "dept", "=", 10),
		conds("salary", ">=", 120),
		conds("dept", "<>", 40, "id", "<=", 20),
		conds("name", ">", "s", "dept", ">=", 10),
		conds("dept", "=", nil),
	}
	for _, cs := range lists {
		p := planScan(t, db, ScanRequest{Table: "emp", Conds: cs})
		got, err := exec.Collect(p.Iter)
		if err != nil {
			t.Fatal(err)
		}
		want := filterRows(emp50(), func(r types.Row) bool {
			for _, c := range cs {
				if !cmpOp(r[empSchema().Index(c.Col)], c.Op, c.Val) {
					return false
				}
			}
			return true
		})
		testkit.AssertRowsUnordered(t, got, want)
	}
}

func TestP11_14_PlanScan_Errors(t *testing.T) {
	db := company(t, Options{})
	tx := db.Begin(ReadCommitted)
	snap, _ := tx.Snapshot()
	tests := []struct {
		req  ScanRequest
		want error
	}{
		{ScanRequest{Table: "nope"}, ErrNoSuchTable},
		{ScanRequest{Table: "emp", Conds: conds("nope", "=", 1)}, ErrNoSuchColumn},
		{ScanRequest{Table: "emp", Conds: conds("dept", "~", 1)}, ErrBadOperator},
		{ScanRequest{Table: "emp", Conds: conds("dept", "=", "ten")}, errTypeMismatch},
		{ScanRequest{Table: "emp", Conds: conds("dept", "=", 1), Need: []string{"nope"}}, ErrNoSuchColumn},
	}
	for _, tc := range tests {
		if _, err := tx.PlanScan(snap, tc.req); !errors.Is(err, tc.want) {
			t.Fatalf("PlanScan(%+v): err = %v, want %v", tc.req, err, tc.want)
		}
	}
}

// joinOracle computes emp ⨝ dept ON emp.dept = dept.id the slow way.
func joinOracle(typ JoinType) []types.Row {
	emps, depts := emp50(), dept5()
	match := func(e, d types.Row) bool { return cmpOp(e[2], "=", d[0]) }
	var out []types.Row
	matchedDept := make([]bool, len(depts))
	for _, e := range emps {
		found := false
		for j, d := range depts {
			if match(e, d) {
				found = true
				matchedDept[j] = true
				if typ != SemiJoin && typ != AntiJoin {
					out = append(out, append(slices.Clone(e), d...))
				}
			}
		}
		switch {
		case typ == SemiJoin && found, typ == AntiJoin && !found:
			out = append(out, e)
		case !found && (typ == LeftJoin || typ == FullJoin):
			out = append(out, append(slices.Clone(e), types.Null(), types.Null()))
		}
	}
	if typ == RightJoin || typ == FullJoin {
		for j, d := range depts {
			if !matchedDept[j] {
				out = append(out, append(types.Row{types.Null(), types.Null(), types.Null(), types.Null()}, d...))
			}
		}
	}
	return out
}

func TestP11_15_PlanJoin_AllTypes(t *testing.T) {
	db := company(t, Options{})
	for _, typ := range []JoinType{InnerJoin, LeftJoin, RightJoin, FullJoin, SemiJoin, AntiJoin} {
		t.Run(typ.String(), func(t *testing.T) {
			l := planScan(t, db, ScanRequest{Table: "emp"})
			r := planScan(t, db, ScanRequest{Table: "dept"})
			p, err := PlanJoin(l, r, typ, "dept", "dept.id")
			if err != nil {
				t.Fatal(err)
			}
			got, err := exec.Collect(p.Iter)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, joinOracle(typ))
			wantCols := []string{"emp.id", "emp.name", "emp.dept", "emp.salary", "dept.id", "dept.name"}
			if typ == SemiJoin || typ == AntiJoin {
				wantCols = wantCols[:4]
			}
			if !slices.Equal(p.Cols, wantCols) {
				t.Fatalf("cols = %v, want %v", p.Cols, wantCols)
			}
		})
	}
}

func TestP11_15_PlanJoin_Explain(t *testing.T) {
	db := company(t, Options{})
	tests := []struct {
		typ  JoinType
		want string
	}{
		{InnerJoin, "Hash Join\n  Hash Cond: (emp.dept = dept.id)\n  ->  Seq Scan on emp\n  ->  Hash\n        ->  Seq Scan on dept"},
		{LeftJoin, "Hash Left Join\n  Hash Cond: (emp.dept = dept.id)\n  ->  Seq Scan on emp\n  ->  Hash\n        ->  Seq Scan on dept"},
		{RightJoin, "Hash Right Join\n  Hash Cond: (emp.dept = dept.id)\n  ->  Seq Scan on emp\n  ->  Hash\n        ->  Seq Scan on dept"},
		{FullJoin, "Hash Full Join\n  Hash Cond: (emp.dept = dept.id)\n  ->  Seq Scan on emp\n  ->  Hash\n        ->  Seq Scan on dept"},
		{AntiJoin, "Nested Loop Anti Join\n  Join Filter: (emp.dept = dept.id)\n  ->  Seq Scan on emp\n  ->  Seq Scan on dept"},
		{SemiJoin, "Nested Loop Semi Join\n  Join Filter: (emp.dept = dept.id)\n  ->  Seq Scan on emp\n  ->  Seq Scan on dept"},
	}
	for _, tc := range tests {
		p, err := PlanJoin(planScan(t, db, ScanRequest{Table: "emp"}), planScan(t, db, ScanRequest{Table: "dept"}), tc.typ, "emp.dept", "dept.id")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Node.String(); got != tc.want {
			t.Fatalf("%s:\n%s\nwant:\n%s", tc.typ, got, tc.want)
		}
	}
}

func TestP11_15_PlanJoin_Errors(t *testing.T) {
	db := company(t, Options{})
	l, r := planScan(t, db, ScanRequest{Table: "emp"}), planScan(t, db, ScanRequest{Table: "dept"})
	j, err := PlanJoin(l, r, LeftJoin, "dept", "id") // unqualified is fine: unique on each side
	if err != nil {
		t.Fatalf("PlanJoin(dept, id): %v", err)
	}
	if _, err := PlanJoin(j, planScan(t, db, ScanRequest{Table: "dept"}), InnerJoin, "id", "dept.id"); !errors.Is(err, ErrAmbiguousColumn) {
		t.Fatalf("\"id\" over emp.id and dept.id: err = %v, want ErrAmbiguousColumn", err)
	}
	if _, err := PlanJoin(l, r, LeftJoin, "nope", "dept.id"); !errors.Is(err, ErrNoSuchColumn) {
		t.Fatalf("unknown left: err = %v, want ErrNoSuchColumn", err)
	}
	if _, err := PlanJoin(l, r, JoinType(42), "emp.dept", "dept.id"); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("bad type: err = %v, want ErrBadQuery", err)
	}
}

func query(t testing.TB, db *DB, q *Query) ([]types.Row, *Plan) {
	t.Helper()
	tx := db.Begin(ReadCommitted)
	defer tx.finish()
	p, err := tx.PlanQuery(q)
	if err != nil {
		t.Fatalf("PlanQuery: %v", err)
	}
	rows, err := exec.Collect(p.Iter)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rows, p
}

func TestP11_16_PlanQuery_SortLimitSelect(t *testing.T) {
	db := company(t, Options{})
	rows, p := query(t, db, Scan("emp").Where("dept", "=", 10).OrderBy("name").Limit(2).Select("name"))
	want := "Limit\n  ->  Sort\n        Sort Key: emp.name\n        Sort Method: top-N heapsort\n        ->  Index Only Scan using emp_dept on emp\n              Index Cond: (emp.dept = 10)"
	if got := p.Node.String(); got != want {
		t.Fatalf("plan:\n%s\nwant:\n%s", got, want)
	}
	if !slices.Equal(p.Cols, []string{"emp.name"}) {
		t.Fatalf("cols = %v", p.Cols)
	}
	testkit.AssertRows(t, rows, []types.Row{R("dmitri"), R("erin")})

	// ORDER BY salary DESC, id OFFSET 3 LIMIT 4 vs the oracle.
	rows, p = query(t, db, Scan("emp").OrderByDesc("salary").OrderBy("id").Offset(3).Limit(4).Select("id", "salary"))
	all := slices.Clone(emp50())
	slices.SortStableFunc(all, func(a, b types.Row) int {
		if c := types.Compare(b[3], a[3]); c != 0 {
			return c
		}
		return types.Compare(a[0], b[0])
	})
	testkit.AssertRows(t, rows, project(all[3:7], 0, 3))
	if !strings.Contains(p.Node.String(), "Sort Key: emp.salary DESC, emp.id") {
		t.Fatalf("plan:\n%s", p.Node.String())
	}

	// No LIMIT → full sort; NULL depts last in ASC.
	rows, p = query(t, db, Scan("emp").OrderBy("dept", "id").Select("dept"))
	if strings.Contains(p.Node.String(), "top-N") || strings.HasPrefix(p.Node.String(), "Limit") {
		t.Fatalf("plan without LIMIT:\n%s", p.Node.String())
	}
	if len(rows) != 50 || !rows[49][0].IsNull() || !rows[45][0].IsNull() || rows[44][0].IsNull() {
		t.Fatalf("ORDER BY dept: 5 NULLs must come last, got %v", rows[40:])
	}
}

func TestP11_16_PlanQuery_GroupBy(t *testing.T) {
	db := company(t, Options{})
	rows, p := query(t, db, Scan("emp").GroupBy("dept").Aggregate(CountStar(), Sum("salary"), Max("name")).OrderBy("dept"))
	want := "Sort\n  Sort Key: emp.dept\n  ->  HashAggregate\n        Group Key: emp.dept\n        ->  Seq Scan on emp"
	if got := p.Node.String(); got != want {
		t.Fatalf("plan:\n%s\nwant:\n%s", got, want)
	}
	if !slices.Equal(p.Cols, []string{"emp.dept", "count(*)", "sum(salary)", "max(name)"}) {
		t.Fatalf("cols = %v", p.Cols)
	}
	// oracle
	type agg struct {
		n   int64
		sum int64
		max string
	}
	groups := map[string]*agg{}
	var keys []types.Value
	for _, r := range emp50() {
		k := r[2].HashKey()
		if groups[k] == nil {
			groups[k] = &agg{}
			keys = append(keys, r[2])
		}
		g := groups[k]
		g.n++
		g.sum += r[3].I
		g.max = max(g.max, r[1].S)
	}
	var wantRows []types.Row
	for _, k := range keys {
		g := groups[k.HashKey()]
		wantRows = append(wantRows, types.Row{k, types.Int64(g.n), types.Int64(g.sum), types.Text(g.max)})
	}
	testkit.AssertRows(t, rows, sortRows(wantRows, 0))

	rows, p = query(t, db, Scan("emp").Where("dept", "=", 10).Aggregate(CountStar()))
	if p.Node.Name != "Aggregate" || !strings.Contains(p.Node.String(), "Index Only Scan using emp_dept") {
		t.Fatalf("COUNT(*) WHERE dept = 10:\n%s\nwant Aggregate over an Index Only Scan", p.Node.String())
	}
	testkit.AssertRows(t, rows, []types.Row{R(9)})
}

func TestP11_16_PlanQuery_JoinGroupOrder(t *testing.T) {
	db := company(t, Options{})
	q := Scan("emp").Where("salary", ">=", 60).
		Join(Scan("dept").Where("name", "<>", "hr"), InnerJoin, "dept", "dept.id").
		GroupBy("dept.name").Aggregate(CountStar(), Avg("salary")).
		OrderByDesc("count(*)").OrderBy("dept.name").Limit(2)
	rows, p := query(t, db, q)
	want := strings.Join([]string{
		"Limit",
		"  ->  Sort",
		"        Sort Key: count(*) DESC, dept.name",
		"        Sort Method: top-N heapsort",
		"        ->  HashAggregate",
		"              Group Key: dept.name",
		"              ->  Hash Join",
		"                    Hash Cond: (emp.dept = dept.id)",
		"                    ->  Seq Scan on emp",
		"                          Filter: (emp.salary >= 60)",
		"                    ->  Hash",
		"                          ->  Seq Scan on dept",
		"                                Filter: (dept.name <> 'hr')",
	}, "\n")
	if got := p.Node.String(); got != want {
		t.Fatalf("plan:\n%s\nwant:\n%s", got, want)
	}
	// oracle
	names := map[int64]string{10: "eng", 20: "sales", 30: "ops"}
	cnt, sum := map[string]int64{}, map[string]int64{}
	for _, r := range emp50() {
		if r[3].I < 60 || r[2].IsNull() {
			continue
		}
		if n, ok := names[r[2].I]; ok {
			cnt[n]++
			sum[n] += r[3].I
		}
	}
	var all []types.Row
	for n, c := range cnt {
		all = append(all, types.Row{types.Text(n), types.Int64(c), types.Float64(float64(sum[n]) / float64(c))})
	}
	slices.SortFunc(all, func(a, b types.Row) int {
		if a[1].I != b[1].I {
			return int(b[1].I - a[1].I)
		}
		return strings.Compare(a[0].S, b[0].S)
	})
	testkit.AssertRows(t, rows, all[:2])
}

func TestP11_16_PlanQuery_Errors(t *testing.T) {
	db := company(t, Options{})
	tx := db.Begin(ReadCommitted)
	tests := []struct {
		q    *Query
		want error
	}{
		{Scan("emp").Join(Scan("dept").Limit(1), InnerJoin, "dept", "dept.id"), ErrBadQuery},
		{Scan("emp").Join(Scan("dept"), InnerJoin, "dept", "id").OrderBy("id"), ErrAmbiguousColumn},
		{Scan("emp").OrderBy("nope"), ErrNoSuchColumn},
		{Scan("emp").GroupBy("dept").Aggregate(Sum("nope")), ErrNoSuchColumn},
		{Scan("emp").GroupBy("dept").Select("name"), ErrNoSuchColumn},
		{Scan("nope"), ErrNoSuchTable},
	}
	for i, tc := range tests {
		if _, err := tx.PlanQuery(tc.q); !errors.Is(err, tc.want) {
			t.Fatalf("query %d: err = %v, want %v", i, err, tc.want)
		}
	}
	tx.finish()
	if _, err := tx.PlanQuery(Scan("emp")); !errors.Is(err, ErrTxnClosed) {
		t.Fatalf("closed txn: err = %v, want ErrTxnClosed", err)
	}
	if got := db.Explain(Scan("emp").Where("id", "=", 7)); got != "Index Scan using emp_id on emp\n  Index Cond: (emp.id = 7)" {
		t.Fatalf("Explain = %q", got)
	}
	var _ executor.CmpOp
}
