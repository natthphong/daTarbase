package engine

import (
	"errors"
	"slices"
	"testing"

	executor "github.com/natthphong/daTarbase/phase06_executor"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// vacuumFixture: 50 employees committed by xid 3; xid 4 deletes ids 1..10 and
// commits; xid 5 inserts one row and rolls back.
func vacuumFixture(t testing.TB, rrBeforeDelete bool) (*DB, *Txn) {
	t.Helper()
	db := company(t, Options{})
	var rr *Txn
	if rrBeforeDelete {
		rr = db.Begin(RepeatableRead)
		rr.Query(Scan("emp").Limit(1))
	}
	del := db.Begin(ReadCommitted)
	if n, err := del.Delete(Scan("emp").Where("id", "<=", 10)); n != 10 || err != nil {
		t.Fatalf("Delete = %d, %v", n, err)
	}
	del.Commit()
	loser := db.Begin(ReadCommitted)
	loser.Insert("emp", R(99, "ghost", 10, 1))
	loser.Rollback()
	return db, rr
}

func TestP11_19_Vacuum_RemovesDeadVersions(t *testing.T) {
	db, _ := vacuumFixture(t, false)
	h := db.Heap("emp")
	before, _ := db.Begin(ReadCommitted).Query(Scan("emp").OrderBy("id"))
	end := db.wal.End()

	st, err := db.Vacuum("emp")
	if err != nil {
		t.Fatal(err)
	}
	if st != (VacuumStats{Scanned: 51, Removed: 11, PagesPruned: 1, AllVisible: 1}) {
		t.Fatalf("stats = %+v, want {Scanned:51 Removed:11 PagesPruned:1 AllVisible:1}", st)
	}
	for slot := types.SlotID(1); slot <= 10; slot++ {
		if rawTuple(t, h, types.TID{Page: 0, Slot: slot}) != nil {
			t.Fatalf("slot %d still holds a tuple", slot)
		}
	}
	for _, name := range []string{"emp_dept", "emp_id"} {
		items, _ := leafEntries(db.IndexByName(name).Tree)
		if len(items) != 40 {
			t.Fatalf("%s: %d entries, want 40", name, len(items))
		}
		if err := db.IndexByName(name).Tree.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	recs := heapRecs(t, walRecords(t, db))
	last := recs[len(recs)-1]
	if last.Op != opPrune || !slices.Equal(last.Slots, []types.SlotID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 51}) || db.wal.End() == end {
		t.Fatalf("last heap record = %+v, want a prune of slots 1..10 and 51", last)
	}
	after, _ := db.Begin(ReadCommitted).Query(Scan("emp").OrderBy("id"))
	testkit.AssertRows(t, after, before)

	// The freed slots are reused: the lowest unused slot is 1.
	tx := db.Begin(ReadCommitted)
	tid, err := tx.Insert("emp", R(100, "new", 10, 1))
	if err != nil || tid != (types.TID{Page: 0, Slot: 1}) {
		t.Fatalf("insert after vacuum = %v, %v; want (0,1)", tid, err)
	}
	if h.vm[0] {
		t.Fatal("the insert did not clear the all-visible bit")
	}
}

func TestP11_19_Vacuum_HorizonKeepsRecentlyDead(t *testing.T) {
	db, rr := vacuumFixture(t, true)
	st, err := db.Vacuum("emp")
	if err != nil {
		t.Fatal(err)
	}
	if st.Removed != 1 || st.AllVisible != 0 {
		t.Fatalf("stats = %+v, want Removed 1 (only the aborted insert) and no all-visible page", st)
	}
	rows, _ := rr.Query(Scan("emp").Where("id", "<=", 10).Select("id"))
	if len(rows) != 10 {
		t.Fatalf("the old RR snapshot sees %d of the deleted rows, want 10", len(rows))
	}
	rr.Commit()
	st, _ = db.Vacuum("emp")
	if st.Removed != 10 || st.AllVisible != 1 {
		t.Fatalf("after the RR txn ended: %+v, want Removed 10, AllVisible 1", st)
	}
}

func TestP11_19_Vacuum_EnablesIndexOnlyScans(t *testing.T) {
	db, _ := vacuumFixture(t, false)
	q := Scan("emp").Where("dept", "=", 10).Select("name")
	run := func() ([]types.Row, int) {
		tx := db.Begin(ReadCommitted)
		defer tx.Commit()
		p, err := tx.PlanQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := exec.Collect(p.Iter)
		if err != nil {
			t.Fatal(err)
		}
		return rows, p.Iter.(*executor.Project).Child.(*IndexOnlyScan).HeapFetches
	}
	rows1, fetches := run()
	if fetches == 0 {
		t.Fatal("before VACUUM the visibility map is empty: every entry needs a heap fetch")
	}
	if _, err := db.Vacuum("emp"); err != nil {
		t.Fatal(err)
	}
	rows2, fetches := run()
	if fetches != 0 {
		t.Fatalf("after VACUUM: HeapFetches = %d, want 0", fetches)
	}
	testkit.AssertRows(t, rows2, rows1)
	if _, err := db.Vacuum("nope"); !errors.Is(err, ErrNoSuchTable) {
		t.Fatalf("Vacuum(nope): err = %v, want ErrNoSuchTable", err)
	}
}
