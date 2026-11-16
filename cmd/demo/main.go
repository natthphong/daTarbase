// Command demo runs a narrated tour of the daTarbase engine (phase 11):
// DDL, inserts, indexes, queries with joins / grouping / sorting, EXPLAIN,
// MVCC isolation, UPDATE / DELETE / ROLLBACK, VACUUM, a clean restart and a
// crash with WAL recovery.
//
//	go run ./cmd/demo
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	engine "github.com/natthphong/daTarbase/phase11_engine"
	"github.com/natthphong/daTarbase/pkg/types"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo failed:", err)
		os.Exit(1)
	}
}

func step(title string) {
	fmt.Printf("\n=== %s %s\n", title, strings.Repeat("=", max(0, 70-len(title))))
}

func show(rows []types.Row, err error) error {
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Println("   ", r)
	}
	fmt.Printf("    (%d rows)\n", len(rows))
	return nil
}

func explain(db *engine.DB, q *engine.Query) {
	for _, l := range strings.Split(db.Explain(q), "\n") {
		fmt.Println("    |", l)
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func run() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	dir, err := os.MkdirTemp("", "datarbase-demo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	opts := engine.Options{PoolFrames: 8, NoSync: true}

	step("Open a new database")
	db, err := engine.OpenWith(dir, opts)
	check(err)
	fmt.Println("    data directory:", dir)

	step("CREATE TABLE emp, dept")
	check(db.CreateTable("emp", types.NewSchema("id", types.KindInt32, "name", types.KindText, "dept", types.KindInt32, "salary", types.KindInt64)))
	check(db.CreateTable("dept", types.NewSchema("id", types.KindInt32, "name", types.KindText)))

	step("INSERT 12 employees, 4 departments, 3000 events — one transaction")
	check(db.CreateTable("events", types.NewSchema("id", types.KindInt64, "kind", types.KindText, "amount", types.KindInt64)))
	tx := db.Begin(engine.ReadCommitted)
	emps := []types.Row{
		row(1, "alice", 10, 120), row(2, "bob", 20, 90), row(3, "carol", 10, 135), row(4, "dave", 30, 70),
		row(5, "erin", 20, 95), row(6, "frank", nil, 60), row(7, "grace", 10, 150), row(8, "heidi", 40, 80),
		row(9, "ivan", 30, 75), row(10, "judy", 20, 110), row(11, "kevin", 60, 65), row(12, "laura", 10, 99),
	}
	for _, r := range emps {
		_, err := tx.Insert("emp", r)
		check(err)
	}
	for _, r := range []types.Row{row(10, "eng"), row(20, "sales"), row(30, "ops"), row(50, "legal")} {
		_, err := tx.Insert("dept", r)
		check(err)
	}
	kinds := []string{"click", "view", "purchase"}
	for i := 1; i <= 3000; i++ {
		_, err := tx.Insert("events", types.Row{types.Int64(int64(i)), types.Text(kinds[i%3]), types.Int64(int64(i * 7 % 500))})
		check(err)
	}
	check(tx.Commit())
	fmt.Printf("    events uses %d heap pages\n", db.Heap("events").NumPages())

	step("CREATE INDEX emp_dept ON emp (dept) INCLUDE (name); emp_id ON emp (id)")
	check(db.CreateIndex("emp_dept", "emp", []string{"dept"}, engine.Include("name")))
	check(db.CreateIndex("emp_id", "emp", []string{"id"}))
	check(db.CreateIndex("events_id", "events", []string{"id"}))

	r := db.Begin(engine.ReadCommitted)

	step("SELECT * FROM emp WHERE dept = 10 ORDER BY salary DESC")
	q := engine.Scan("emp").Where("dept", "=", 10).OrderByDesc("salary")
	explain(db, q)
	check(show(r.Query(q)))

	step("Index Only Scan: SELECT name FROM emp WHERE dept = 20")
	q = engine.Scan("emp").Where("dept", "=", 20).Select("name")
	explain(db, q)
	check(show(r.Query(q)))

	step("Range on the second index: id >= 10 AND salary > 70")
	q = engine.Scan("emp").Where("salary", ">", 70).Where("id", ">=", 10)
	explain(db, q)
	check(show(r.Query(q)))

	for _, j := range []struct {
		name string
		typ  engine.JoinType
	}{{"INNER", engine.InnerJoin}, {"LEFT", engine.LeftJoin}, {"RIGHT", engine.RightJoin}, {"FULL", engine.FullJoin}, {"SEMI (EXISTS)", engine.SemiJoin}, {"ANTI (NOT EXISTS)", engine.AntiJoin}} {
		step(j.name + " JOIN emp ⨝ dept ON emp.dept = dept.id")
		q = engine.Scan("emp").Join(engine.Scan("dept"), j.typ, "dept", "dept.id")
		if j.typ == engine.SemiJoin || j.typ == engine.AntiJoin {
			q = q.Select("emp.name", "emp.dept")
		} else {
			q = q.Select("emp.name", "dept.name")
		}
		explain(db, q)
		check(show(r.Query(q)))
	}

	step("GROUP BY dept.name: headcount and average salary, top 3")
	q = engine.Scan("emp").Join(engine.Scan("dept"), engine.InnerJoin, "dept", "dept.id").
		GroupBy("dept.name").Aggregate(engine.CountStar(), engine.Avg("salary"), engine.Max("emp.name")).
		OrderByDesc("count(*)").OrderBy("dept.name").Limit(3)
	explain(db, q)
	check(show(r.Query(q)))

	step("Aggregate over 3000 events with LIMIT/OFFSET on the groups")
	q = engine.Scan("events").Where("amount", ">=", 250).GroupBy("kind").Aggregate(engine.CountStar(), engine.Sum("amount")).OrderBy("kind").Offset(1).Limit(2)
	explain(db, q)
	check(show(r.Query(q)))
	check(r.Commit())

	step("MVCC: READ COMMITTED vs REPEATABLE READ")
	rc := db.Begin(engine.ReadCommitted)
	rr := db.Begin(engine.RepeatableRead)
	count := func(t *engine.Txn) int64 {
		rows, err := t.Query(engine.Scan("emp").Aggregate(engine.CountStar()))
		check(err)
		return rows[0][0].I
	}
	fmt.Printf("    before: RC sees %d, RR sees %d\n", count(rc), count(rr))
	w := db.Begin(engine.ReadCommitted)
	_, err = w.Insert("emp", row(13, "mallory", 20, 85))
	check(err)
	fmt.Printf("    writer inserted mallory (not committed): RC sees %d, RR sees %d, writer sees %d\n", count(rc), count(rr), count(w))
	check(w.Commit())
	fmt.Printf("    writer committed:                         RC sees %d, RR sees %d (snapshot from its first statement)\n", count(rc), count(rr))
	check(rc.Commit())
	check(rr.Commit())

	step("UPDATE emp SET salary = 100 WHERE dept = 20 (new versions, old ones get xmax)")
	u := db.Begin(engine.ReadCommitted)
	n, err := u.Update(engine.Scan("emp").Where("dept", "=", 20), map[string]any{"salary": 100})
	check(err)
	fmt.Printf("    %d rows updated by xid %d\n", n, u.XID())

	step("Write-write conflict: a second transaction updates bob while the first is open")
	c := db.Begin(engine.ReadCommitted)
	_, err = c.Update(engine.Scan("emp").Where("name", "=", "bob"), map[string]any{"salary": 1})
	fmt.Println("    error:", err, "| is ErrWriteConflict:", errors.Is(err, engine.ErrWriteConflict))
	check(c.Rollback())
	check(u.Commit())

	step("DELETE then ROLLBACK: nothing is undone on the page, the xid is just aborted")
	d := db.Begin(engine.ReadCommitted)
	n, err = d.Delete(engine.Scan("emp").Where("dept", "=", 10))
	check(err)
	fmt.Printf("    deleted %d rows inside the transaction, then ROLLBACK\n", n)
	check(d.Rollback())
	r = db.Begin(engine.ReadCommitted)
	check(show(r.Query(engine.Scan("emp").Select("id", "name", "salary").OrderBy("id"))))
	check(r.Commit())

	step("VACUUM emp: remove dead versions, set the visibility map")
	st, err := db.Vacuum("emp")
	check(err)
	fmt.Printf("    %+v\n", st)

	step("Clean shutdown (checkpoint) and reopen")
	check(db.Close())
	db, err = engine.OpenWith(dir, opts)
	check(err)
	fmt.Printf("    recovery after a clean shutdown: %+v\n", db.Recovery)
	r = db.Begin(engine.ReadCommitted)
	check(show(r.Query(engine.Scan("emp").Where("dept", "=", 20).Select("name", "salary"))))
	check(r.Commit())

	step("Crash: one committed transaction, one still running, dirty pages lost")
	ok := db.Begin(engine.ReadCommitted)
	for i := 3001; i <= 3500; i++ {
		_, err := ok.Insert("events", types.Row{types.Int64(int64(i)), types.Text("refund"), types.Int64(5)})
		check(err)
	}
	check(ok.Commit())
	ghost := db.Begin(engine.ReadCommitted)
	for i := 9001; i <= 9200; i++ {
		_, err := ghost.Insert("events", types.Row{types.Int64(int64(i)), types.Text("ghost"), types.Int64(1)})
		check(err)
	}
	db.Crash()
	fmt.Println("    *** power failure ***")

	step("Reopen: crash recovery replays the WAL")
	db, err = engine.OpenWith(dir, opts)
	check(err)
	fmt.Printf("    %+v\n", db.Recovery)
	r = db.Begin(engine.ReadCommitted)
	q = engine.Scan("events").GroupBy("kind").Aggregate(engine.CountStar()).OrderBy("kind")
	explain(db, q)
	check(show(r.Query(q)))
	fmt.Println("    → 500 committed refunds survived, the 200 uncommitted ghosts did not")
	check(r.Commit())
	check(db.Close())
	return nil
}

// row converts Go literals to a row (nil → NULL, int → int8, string → text);
// the engine coerces int8 to the int4 columns.
func row(xs ...any) types.Row {
	out := make(types.Row, len(xs))
	for i, x := range xs {
		switch v := x.(type) {
		case nil:
			out[i] = types.Null()
		case int:
			out[i] = types.Int64(int64(v))
		case string:
			out[i] = types.Text(v)
		}
	}
	return out
}
