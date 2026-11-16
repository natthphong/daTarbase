package engine

import (
	"errors"
	"testing"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// emp3 creates emp with alice (0,1), bob (0,2), carol (0,3), committed, and
// the index emp_dept (dept).
func emp3(t testing.TB) *DB {
	t.Helper()
	db := newDB(t, Options{})
	mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	for _, r := range []types.Row{R(1, "alice", 10, 100), R(2, "bob", 20, 80), R(3, "carol", 10, 120)} {
		if _, err := tx.Insert("emp", r); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

func names(t testing.TB, tx *Txn, q *Query) []types.Row {
	t.Helper()
	rows, err := tx.Query(q.Select("name"))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

var alice = types.TID{Page: 0, Slot: 1}
var bob = types.TID{Page: 0, Slot: 2}

func TestP11_17_Delete_StampsXmax(t *testing.T) {
	db := emp3(t)
	h := db.Heap("emp")
	t1 := db.Begin(ReadCommitted)
	if err := h.Delete(t1, alice); err != nil {
		t.Fatal(err)
	}
	if hdr := rawHeader(t, h, alice); hdr.Xmax != t1.XID() || t1.XID() != 4 {
		t.Fatalf("xmax = %d (txn xid %d), want 4", hdr.Xmax, t1.XID())
	}
	recs := walRecords(t, db)
	hr := heapRecs(t, recs[len(recs)-1:])[0]
	if hr.Op != opDelete || hr.Page != 0 || hr.Slot != 1 || hr.Xmax != 4 || recs[len(recs)-1].Txn != 4 {
		t.Fatalf("last WAL record = %+v, want delete (0,1) xmax 4", hr)
	}
	if got := lsnOf(t, h, 0); got != recs[len(recs)-1].LSN {
		t.Fatalf("page LSN %d, want %d", got, recs[len(recs)-1].LSN)
	}
	testkit.AssertRows(t, names(t, t1, Scan("emp")), []types.Row{R("bob"), R("carol")})
	other := db.Begin(ReadCommitted)
	testkit.AssertRows(t, names(t, other, Scan("emp")), []types.Row{R("alice"), R("bob"), R("carol")})

	end := db.wal.End()
	if err := h.Delete(t1, alice); err != nil || db.wal.End() != end {
		t.Fatalf("second delete by the same txn: err %v, WAL grew %d bytes; want nil and nothing logged", err, db.wal.End()-end)
	}
	k10, _ := tuple.EncodeKey(types.Row{types.Int32(10)}, nil)
	if got := db.IndexByName("emp_dept").Tree.Search(k10); len(got) != 2 {
		t.Fatalf("index entries for dept 10 = %v, want 2 (DELETE leaves them for VACUUM)", got)
	}
}

func TestP11_17_Delete_WriteConflictAndAbortedDeleter(t *testing.T) {
	db := emp3(t)
	h := db.Heap("emp")
	t1 := db.Begin(ReadCommitted)
	t2 := db.Begin(ReadCommitted)
	if err := h.Delete(t1, alice); err != nil {
		t.Fatal(err)
	}
	before := rawTuple(t, h, alice)
	if err := h.Delete(t2, alice); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("t2.Delete(alice) while t1 runs: err = %v, want ErrWriteConflict", err)
	}
	if string(rawTuple(t, h, alice)) != string(before) {
		t.Fatal("a failed delete changed the tuple")
	}
	if err := t1.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := h.Delete(t2, alice); err != nil {
		t.Fatalf("after t1 rolled back: err = %v, want nil", err)
	}
	if hdr := rawHeader(t, h, alice); hdr.Xmax != t2.XID() {
		t.Fatalf("xmax = %d, want t2 (%d)", hdr.Xmax, t2.XID())
	}
}

func TestP11_17_Delete_SerializationAndInvisible(t *testing.T) {
	db := emp3(t)
	h := db.Heap("emp")
	rr := db.Begin(RepeatableRead)
	rc := db.Begin(ReadCommitted)
	testkit.AssertRows(t, names(t, rr, Scan("emp")), []types.Row{R("alice"), R("bob"), R("carol")}) // snapshot taken
	t1 := db.Begin(ReadCommitted)
	if n, err := t1.Delete(Scan("emp").Where("id", "=", 2)); n != 1 || err != nil {
		t.Fatalf("t1 Delete bob = %d, %v", n, err)
	}
	if err := t1.Commit(); err != nil {
		t.Fatal(err)
	}
	if n, err := rr.Delete(Scan("emp").Where("id", "=", 2)); !errors.Is(err, ErrSerialization) || n != 0 {
		t.Fatalf("RR delete of bob = %d, %v; want ErrSerialization", n, err)
	}
	if n, err := rc.Delete(Scan("emp").Where("id", "=", 2)); n != 0 || err != nil {
		t.Fatalf("RC delete of bob = %d, %v; want 0 rows (bob is gone for a new snapshot)", n, err)
	}

	loser := db.Begin(ReadCommitted)
	tid, _ := loser.Insert("emp", R(4, "dave", 30, 1))
	if err := loser.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []types.TID{tid, {Page: 0, Slot: 0}, {Page: 0, Slot: 9}, {Page: 7, Slot: 1}} {
		if err := h.Delete(rc, bad); !errors.Is(err, ErrTupleNotFound) {
			t.Fatalf("Delete(%v): err = %v, want ErrTupleNotFound", bad, err)
		}
	}
	running := db.Begin(ReadCommitted)
	tid, _ = running.Insert("emp", R(5, "erin", 30, 1))
	if err := h.Delete(rc, tid); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("delete of another txn's uncommitted row: err = %v, want ErrTupleNotFound", err)
	}
	if err := h.Delete(running, tid); err != nil {
		t.Fatalf("delete of my own uncommitted row: %v", err)
	}
}

func TestP11_18_Update_NewVersions(t *testing.T) {
	db := emp3(t)
	h := db.Heap("emp")
	tx := db.Begin(ReadCommitted)
	n, err := tx.Update(Scan("emp").Where("dept", "=", 10), map[string]any{"dept": 30})
	if err != nil || n != 2 {
		t.Fatalf("Update = %d, %v; want 2", n, err)
	}
	for _, tid := range []types.TID{alice, {Page: 0, Slot: 3}} {
		if hdr := rawHeader(t, h, tid); hdr.Xmax != tx.XID() {
			t.Fatalf("old version %v xmax = %d, want %d", tid, hdr.Xmax, tx.XID())
		}
	}
	for i, tid := range []types.TID{{Page: 0, Slot: 4}, {Page: 0, Slot: 5}} {
		b := rawTuple(t, h, tid)
		row, _ := tuple.DeformTuple(empSchema(), b)
		want := []types.Row{R(int32(1), "alice", int32(30), 100), R(int32(3), "carol", int32(30), 120)}[i]
		testkit.AssertRows(t, []types.Row{row}, []types.Row{want})
	}
	if items, _ := leafEntries(db.IndexByName("emp_dept").Tree); len(items) != 5 {
		t.Fatalf("emp_dept has %d entries, want 5 (no HOT: every new version is indexed)", len(items))
	}
	testkit.AssertRows(t, names(t, tx, Scan("emp").Where("dept", "=", 10)), nil)
	testkit.AssertRows(t, names(t, tx, Scan("emp").Where("dept", "=", 30)), []types.Row{R("alice"), R("carol")})
	other := db.Begin(ReadCommitted)
	testkit.AssertRows(t, names(t, other, Scan("emp").Where("dept", "=", 10)), []types.Row{R("alice"), R("carol")})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, names(t, other, Scan("emp").Where("dept", "=", 30)), []types.Row{R("alice"), R("carol")})
}

// Without collecting the targets first, the new versions (dept 40 ≥ 30) would
// be found again by the same scan.
func TestP11_18_Update_Halloween(t *testing.T) {
	db := company(t, Options{})
	tx := db.Begin(ReadCommitted)
	want := len(filterRows(emp50(), func(r types.Row) bool { return cmpOp(r[2], ">=", types.Int32(30)) }))
	n, err := tx.Update(Scan("emp").Where("dept", ">=", 30), map[string]any{"dept": 40, "salary": 1})
	if err != nil || n != want {
		t.Fatalf("Update = %d, %v; want %d", n, err, want)
	}
	rows, _ := tx.Query(Scan("emp").Where("dept", "=", 40).Aggregate(CountStar(), Sum("salary")))
	testkit.AssertRows(t, rows, []types.Row{R(want, want)})
}

func TestP11_18_Update_ConflictsAndRollback(t *testing.T) {
	db := emp3(t)
	t1 := db.Begin(ReadCommitted)
	t2 := db.Begin(ReadCommitted)
	if n, err := t1.Update(Scan("emp").Where("name", "=", "alice"), map[string]any{"salary": 1}); n != 1 || err != nil {
		t.Fatalf("t1 Update = %d, %v", n, err)
	}
	if n, err := t2.Update(Scan("emp").Where("id", "=", 1), map[string]any{"salary": 2}); !errors.Is(err, ErrWriteConflict) || n != 0 {
		t.Fatalf("t2 Update while t1 runs = %d, %v; want ErrWriteConflict", n, err)
	}
	if err := t1.Rollback(); err != nil {
		t.Fatal(err)
	}
	t3 := db.Begin(ReadCommitted)
	rows, _ := t3.Query(Scan("emp").Select("name", "salary").OrderBy("id"))
	testkit.AssertRows(t, rows, []types.Row{R("alice", 100), R("bob", 80), R("carol", 120)})

	rr := db.Begin(RepeatableRead)
	rr.Query(Scan("emp"))
	if _, err := t3.Update(Scan("emp").Where("id", "=", 2), map[string]any{"salary": 81}); err != nil {
		t.Fatal(err)
	}
	t3.Commit()
	if _, err := rr.Update(Scan("emp").Where("id", "=", 2), map[string]any{"salary": 82}); !errors.Is(err, ErrSerialization) {
		t.Fatalf("RR update of a row updated after its snapshot: err = %v, want ErrSerialization", err)
	}
}

func TestP11_18_Update_ValidatesFirst(t *testing.T) {
	db := emp3(t)
	tx := db.Begin(ReadCommitted)
	tests := []struct {
		q    *Query
		set  map[string]any
		want error
	}{
		{Scan("emp").Limit(1), map[string]any{"salary": 1}, ErrBadQuery},
		{Scan("nope"), map[string]any{"salary": 1}, ErrNoSuchTable},
		{Scan("emp"), map[string]any{"salary": 1, "nope": 2}, ErrNoSuchColumn},
		{Scan("emp"), map[string]any{"dept": "ten"}, errTypeMismatch},
		{Scan("emp"), map[string]any{"emp.dept": 1 << 40}, ErrOutOfRange},
	}
	for i, tc := range tests {
		if n, err := tx.Update(tc.q, tc.set); !errors.Is(err, tc.want) || n != 0 {
			t.Fatalf("case %d: Update = %d, %v; want %v", i, n, err, tc.want)
		}
	}
	if tx.XID() != 0 {
		t.Fatalf("failed updates assigned xid %d", tx.XID())
	}
	if n, err := tx.Update(Scan("emp").Where("emp.id", ">", 1), map[string]any{"emp.salary": 0}); n != 2 || err != nil {
		t.Fatalf("qualified names: Update = %d, %v; want 2", n, err)
	}
}
