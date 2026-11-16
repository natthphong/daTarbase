package engine

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	executor "github.com/natthphong/daTarbase/phase06_executor"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func padSchema() types.Schema { return types.NewSchema("id", types.KindInt32, "pad", types.KindText) }

func padRow(id int32, n int) types.Row {
	return types.Row{types.Int32(id), types.Text(strings.Repeat("x", n))}
}

func TestP11_4_Insert_PagePlacementAndFSM(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "pads", padSchema())
	tx := beginXid(db, ReadCommitted)

	want := mkTIDs(0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6, 0, 7, 1, 1)
	for i, w := range want {
		tid, err := h.Insert(tx.XID(), padRow(int32(i+1), 1000)) // 1024-byte tuple
		if err != nil || tid != w {
			t.Fatalf("row %d: Insert = %v, %v; want %v", i+1, tid, err, w)
		}
	}
	f, _ := h.pool.FetchPage(0)
	p := heapPage(f)
	if p.Lower() != 40 || p.Upper() != 1016 || p.FreeSpace() != 972 {
		t.Fatalf("page 0 lower/upper/free = %d/%d/%d, want 40/1016/972", p.Lower(), p.Upper(), p.FreeSpace())
	}
	h.pool.UnpinPage(0, false)
	if h.fsm.Category(0) != 30 || h.fsm.Category(1) != 223 {
		t.Fatalf("FSM categories = %d, %d; want 30 (972/32), 223 (7140/32)", h.fsm.Category(0), h.fsm.Category(1))
	}

	tid, err := h.Insert(tx.XID(), padRow(9, 500)) // 524-byte tuple fits page 0's hole
	if err != nil || tid != (types.TID{Page: 0, Slot: 8}) {
		t.Fatalf("small row: Insert = %v, %v; want (0,8) (the FSM must send it back to page 0)", tid, err)
	}
	tid, err = h.Insert(tx.XID(), padRow(10, 8144)) // 8168 bytes: exactly MaxTupleLen
	if err != nil || tid != (types.TID{Page: 2, Slot: 1}) {
		t.Fatalf("exact-fit row: Insert = %v, %v; want (2,1)", tid, err)
	}
	if h.NumPages() != 3 {
		t.Fatalf("NumPages = %d, want 3", h.NumPages())
	}
}

func TestP11_4_Insert_TupleBytesAndWAL(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	tx := beginXid(db, ReadCommitted) // xid 3
	row := types.Row{types.Int32(1), types.Text("alice"), types.Int32(10), types.Int64(100)}
	before := db.wal.End()
	tid, err := h.Insert(tx.XID(), row)
	if err != nil || tid != (types.TID{Page: 0, Slot: 1}) {
		t.Fatalf("Insert = %v, %v; want (0,1)", tid, err)
	}
	want, _ := tuple.FormTuple(empSchema(), row, 3)
	if got := rawTuple(t, h, tid); !bytes.Equal(got, want) {
		t.Fatalf("stored tuple = % x\nwant FormTuple(row, xmin 3) = % x", got, want)
	}
	recs := walRecords(t, db)
	last := recs[len(recs)-1]
	if last.LSN != before || last.Txn != 3 {
		t.Fatalf("WAL record LSN %d txn %d, want LSN %d (the old end) txn 3", last.LSN, last.Txn, before)
	}
	hr := heapRecs(t, recs[len(recs)-1:])[0]
	if hr.Op != opInsert || hr.Rel != 16384 || hr.Page != 0 || hr.Slot != 1 || !bytes.Equal(hr.Tuple, want) {
		t.Fatalf("WAL body = %+v, want insert rel 16384 (0,1) with the tuple bytes", hr)
	}
	if got := lsnOf(t, h, 0); got != last.LSN {
		t.Fatalf("page LSN = %d, want %d (the insert record)", got, last.LSN)
	}
	if db.wal.FlushedLSN() > before {
		t.Fatalf("Insert flushed the WAL (flushed %d): flushing is Commit's job", db.wal.FlushedLSN())
	}
	if !db.Heap("emp").pool.IsDirty(0) {
		t.Fatal("page 0 is not dirty after the insert")
	}
	if pins, _ := h.pool.PinCount(0); pins != 0 {
		t.Fatalf("page 0 pin count = %d, want 0", pins)
	}
}

func TestP11_4_Insert_ErrorsChangeNothing(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "pads", padSchema())
	tx := beginXid(db, ReadCommitted)
	end := db.wal.End()
	tests := []struct {
		name string
		row  types.Row
		want error
	}{
		{"one byte too large", padRow(1, 8145), ErrTupleTooLarge},
		{"wrong kind", testkit.R("x", "y"), tuple.ErrTypeMismatch},
		{"wrong arity", types.Row{types.Int32(1)}, tuple.ErrArity},
	}
	for _, tc := range tests {
		if _, err := h.Insert(tx.XID(), tc.row); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if h.NumPages() != 0 || db.wal.End() != end {
		t.Fatalf("after failed inserts: %d pages, WAL end %d; want 0 pages, WAL end %d", h.NumPages(), db.wal.End(), end)
	}
}

func TestP11_5_SeqScan_Visibility(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	a := beginXid(db, ReadCommitted) // 3
	b := beginXid(db, ReadCommitted) // 4
	tids := rawInsert(t, a, h, R(int32(1), "alice", int32(10), 100), R(int32(3), "carol", int32(10), 120))
	rawInsert(t, b, h, R(int32(2), "bob", int32(20), 80))
	commitRaw(t, a)
	abortRaw(t, b)
	c := beginXid(db, ReadCommitted) // 5 deletes carol and commits
	stampXmax(t, h, tids[1], c.XID())
	commitRaw(t, c)

	reader := db.Begin(ReadCommitted)
	s := &SeqScan{Heap: h, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog, Mine: reader.mine()}
	rows, err := exec.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, rows, []types.Row{R(int32(1), "alice", int32(10), 100)})
	if s.PagesRead != 1 {
		t.Fatalf("PagesRead = %d, want 1", s.PagesRead)
	}

	s.EmitTID = true
	rows, _ = exec.Collect(s)
	testkit.AssertRows(t, rows, []types.Row{R(int32(1), "alice", int32(10), 100, 1)})
}

func TestP11_5_SeqScan_OwnChangesAndOthers(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	w := db.Begin(ReadCommitted)
	rawInsert(t, w, h, R(int32(1), "alice", int32(10), 100))
	if got := seqRows(t, w, h); len(got) != 1 {
		t.Fatalf("writer sees %d rows of its own, want 1", len(got))
	}
	other := db.Begin(ReadCommitted)
	if got := seqRows(t, other, h); len(got) != 0 {
		t.Fatalf("another transaction sees %d uncommitted rows, want 0", len(got))
	}
	commitRaw(t, w)
	if got := seqRows(t, other, h); len(got) != 1 {
		t.Fatalf("after commit (new snapshot) other sees %d rows, want 1", len(got))
	}
}

func TestP11_5_SeqScan_PageAtATimeAndLimit(t *testing.T) {
	db := newDB(t, Options{PoolFrames: 8})
	h := mustCreate(t, db, "events", eventsSchema())
	tx := db.Begin(ReadCommitted)
	ev := events5000()
	rawInsert(t, tx, h, ev...)
	commitRaw(t, tx)

	s := &SeqScan{Heap: h, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog}
	rows, err := exec.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, rows, ev)
	if s.PagesRead != int(h.NumPages()) || h.NumPages() != 29 {
		t.Fatalf("PagesRead = %d, NumPages = %d; want 29 and 29", s.PagesRead, h.NumPages())
	}
	for _, id := range h.pool.Resident() {
		if pins, _ := h.pool.PinCount(id); pins != 0 {
			t.Fatalf("page %d still pinned after the scan", id)
		}
	}

	lim := &executor.Limit{Child: s, Count: 1}
	rows, _ = exec.Collect(lim)
	if len(rows) != 1 || s.PagesRead != 1 {
		t.Fatalf("LIMIT 1: %d rows, PagesRead %d; want 1 row from 1 page", len(rows), s.PagesRead)
	}

	fresh := &SeqScan{Heap: h, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog}
	if _, _, err := fresh.Next(); !errors.Is(err, exec.ErrNotOpen) {
		t.Fatalf("Next before Open: err = %v, want exec.ErrNotOpen", err)
	}
}
