package engine

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	btree "github.com/natthphong/daTarbase/phase05_btree"
	executor "github.com/natthphong/daTarbase/phase06_executor"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP11_8_Entry(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	ix, err := newIndex(&IndexDef{Name: "emp_dept", Table: "emp", Columns: []string{"dept"}, Include: []string{"name"}}, h, 64)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		row     types.Row
		tid     types.TID
		key     []byte
		payload types.Row
	}{
		{"value key", R(int32(7), "gus", int32(20), 95), types.TID{Page: 0, Slot: 7}, []byte{0x01, 0x80, 0, 0, 0x14}, R(int32(20), "gus")},
		{"NULL key sorts last", R(int32(8), "hal", nil, 90), types.TID{Page: 0, Slot: 8}, []byte{0x02}, R(nil, "hal")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := slices.Clone(tc.row)
			it, err := ix.Entry(tc.row, tc.tid)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(it.Key, tc.key) || it.TID != tc.tid {
				t.Fatalf("Key % x TID %v, want % x %v", it.Key, it.TID, tc.key, tc.tid)
			}
			got, err := tuple.DeformTuple(ix.PayloadSchema, it.Payload)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, []types.Row{got}, []types.Row{tc.payload})
			hdr, _ := tuple.DecodeHeader(it.Payload)
			if hdr.Xmin != 0 {
				t.Fatalf("payload xmin = %d, want 0", hdr.Xmin)
			}
			testkit.AssertRows(t, []types.Row{tc.row}, []types.Row{orig})
		})
	}
}

func TestP11_8_Entry_CompositeKey(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	ix, _ := newIndex(&IndexDef{Name: "emp_dn", Table: "emp", Columns: []string{"dept", "name"}}, h, 64)
	it, err := ix.Entry(R(int32(1), "ab", int32(10), 5), types.TID{Page: 3, Slot: 4})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := tuple.EncodeKey(types.Row{types.Int32(10), types.Text("ab")}, nil)
	if !bytes.Equal(it.Key, want) {
		t.Fatalf("Key = % x, want % x", it.Key, want)
	}
	if got := ix.Columns(); !slices.Equal(got, []string{"emp.dept", "emp.name"}) {
		t.Fatalf("Columns = %v", got)
	}
}

// leafEntries walks the leaf chain left to right.
func leafEntries(tr *btree.Tree) (items []btree.Item, leafSizes []int) {
	n := tr.Store.Peek(tr.Root)
	for !n.Leaf {
		n = tr.Store.Peek(n.Children[0])
	}
	for {
		items = append(items, n.Items...)
		leafSizes = append(leafSizes, len(n.Items))
		if n.Next == types.InvalidPageID {
			return
		}
		n = tr.Store.Peek(n.Next)
	}
}

func TestP11_9_Build_IncludesEveryVersion(t *testing.T) {
	db := newDB(t, Options{IndexFanout: 4})
	h := mustCreate(t, db, "emp", empSchema())
	tx := db.Begin(ReadCommitted)
	tids := rawInsert(t, tx, h, R(int32(1), "alice", int32(10), 100), R(int32(2), "bob", int32(20), 80), R(int32(3), "carol", int32(10), 120))
	commitRaw(t, tx)
	del := beginXid(db, ReadCommitted)
	stampXmax(t, h, tids[2], del.XID())
	commitRaw(t, del)

	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}); err != nil {
		t.Fatal(err)
	}
	ix := db.IndexByName("emp_dept")
	if err := ix.Tree.Validate(); err != nil || ix.Tree.Height != 1 {
		t.Fatalf("Validate = %v, Height %d; want valid, 1", err, ix.Tree.Height)
	}
	items, _ := leafEntries(ix.Tree)
	k10, _ := tuple.EncodeKey(types.Row{types.Int32(10)}, nil)
	k20, _ := tuple.EncodeKey(types.Row{types.Int32(20)}, nil)
	want := []btree.Item{{Key: k10, TID: types.TID{Page: 0, Slot: 1}}, {Key: k10, TID: types.TID{Page: 0, Slot: 3}}, {Key: k20, TID: types.TID{Page: 0, Slot: 2}}}
	if len(items) != len(want) {
		t.Fatalf("%d entries, want 3 (the deleted carol too)", len(items))
	}
	for i := range want {
		if !bytes.Equal(items[i].Key, want[i].Key) || items[i].TID != want[i].TID {
			t.Fatalf("entry %d = (% x, %v), want (% x, %v)", i, items[i].Key, items[i].TID, want[i].Key, want[i].TID)
		}
	}
	for _, id := range h.pool.Resident() {
		if pins, _ := h.pool.PinCount(id); pins != 0 {
			t.Fatalf("Build left page %d pinned", id)
		}
	}
}

func TestP11_9_Build_FiftyRowsAndEmpty(t *testing.T) {
	db := newDB(t, Options{IndexFanout: 4})
	h := mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_id", "emp", []string{"id"}); err != nil {
		t.Fatal(err)
	}
	if tr := db.IndexByName("emp_id").Tree; tr.Height != 1 || len(tr.Store.Peek(tr.Root).Items) != 0 {
		t.Fatalf("empty table: Height %d, root items %d; want an empty root leaf", tr.Height, len(tr.Store.Peek(tr.Root).Items))
	}

	tx := db.Begin(ReadCommitted)
	rawInsert(t, tx, h, emp50()...)
	if err := db.CreateIndex("emp_name", "emp", []string{"name"}, Include("salary")); err != nil {
		t.Fatal(err)
	}
	ix := db.IndexByName("emp_name")
	if err := ix.Tree.Validate(); err != nil {
		t.Fatal(err)
	}
	items, sizes := leafEntries(ix.Tree)
	if len(items) != 50 {
		t.Fatalf("%d entries, want 50 (uncommitted rows are indexed too)", len(items))
	}
	wantSizes := append(slices.Repeat([]int{3}, 16), 2)
	if !slices.Equal(sizes, wantSizes) {
		t.Fatalf("leaf sizes = %v, want %v (BulkLoad, fillfactor 90)", sizes, wantSizes)
	}
	names := slices.Clone(empNames)
	slices.Sort(names)
	first, _ := tuple.DeformTuple(ix.PayloadSchema, items[0].Payload)
	if first[0].S != names[0] {
		t.Fatalf("first entry = %v, want name %q", first, names[0])
	}

	if err := db.CreateIndex("bad", "emp", []string{"nope"}); !errors.Is(err, ErrNoSuchColumn) {
		t.Fatalf("unknown column: err = %v, want ErrNoSuchColumn", err)
	}
	if err := db.CreateIndex("emp_name", "emp", []string{"id"}); !errors.Is(err, ErrIndexExists) {
		t.Fatalf("duplicate name: err = %v, want ErrIndexExists", err)
	}
}

func TestP11_10_Insert_MaintainsIndexes(t *testing.T) {
	db := newDB(t, Options{})
	mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateIndex("emp_name", "emp", []string{"name"}, Include("id")); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	tid, err := tx.Insert("emp", R(1, "alice", 10, 100)) // int8 literals, coerced to int4
	if err != nil || tid != (types.TID{Page: 0, Slot: 1}) {
		t.Fatalf("Insert = %v, %v; want (0,1)", tid, err)
	}
	k10, _ := tuple.EncodeKey(types.Row{types.Int32(10)}, nil)
	if got := db.IndexByName("emp_dept").Tree.Search(k10); !slices.Equal(got, []types.TID{tid}) {
		t.Fatalf("emp_dept.Search(10) = %v, want [%v]", got, tid)
	}
	kAlice, _ := tuple.EncodeKey(types.Row{types.Text("alice")}, nil)
	if got := db.IndexByName("emp_name").Tree.Search(kAlice); !slices.Equal(got, []types.TID{tid}) {
		t.Fatalf("emp_name.Search('alice') = %v, want [%v]", got, tid)
	}
	hdr := rawHeader(t, db.Heap("emp"), tid)
	if hdr.Xmin != 3 {
		t.Fatalf("xmin = %d, want 3", hdr.Xmin)
	}
	if got := seqRows(t, tx, db.Heap("emp")); len(got) != 1 || got[0][0].Kind != types.KindInt32 {
		t.Fatalf("stored row = %v, want id stored as int4", got)
	}
}

func TestP11_10_Insert_Errors(t *testing.T) {
	db := newDB(t, Options{})
	mustCreate(t, db, "emp", empSchema())
	tx := db.Begin(ReadCommitted)
	tests := []struct {
		name string
		tbl  string
		row  types.Row
		want error
	}{
		{"wrong arity", "emp", R(2, "bob"), tuple.ErrArity},
		{"int4 overflow", "emp", R(3000000000, "x", 1, 1), ErrOutOfRange},
		{"text into int", "emp", R("x", "x", 1, 1), tuple.ErrTypeMismatch},
		{"unknown table", "nope", R(1), ErrNoSuchTable},
	}
	for _, tc := range tests {
		if _, err := tx.Insert(tc.tbl, tc.row); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if tx.XID() != 0 {
		t.Fatalf("failed inserts assigned xid %d", tx.XID())
	}
	tx.finish()
	if _, err := tx.Insert("emp", R(1, "a", 1, 1)); !errors.Is(err, ErrTxnClosed) {
		t.Fatalf("Insert after end: err = %v, want ErrTxnClosed", err)
	}
}

func TestP11_11_KeyBounds_Bytes(t *testing.T) {
	enc := func(v types.Value) []byte { b, _ := tuple.EncodeKey(types.Row{v}, nil); return b }
	tests := []struct {
		name   string
		op     executor.CmpOp
		v      types.Value
		lo, hi btree.Bound
	}{
		{"= 10", executor.OpEQ, types.Int32(10), btree.Incl(enc(types.Int32(10))), btree.Excl([]byte{1, 0x80, 0, 0, 0x0b})},
		{"< 10", executor.OpLT, types.Int32(10), btree.NoBound(), btree.Excl(enc(types.Int32(10)))},
		{"<= 10", executor.OpLE, types.Int32(10), btree.NoBound(), btree.Excl([]byte{1, 0x80, 0, 0, 0x0b})},
		{"> 255 drops trailing ff", executor.OpGT, types.Int32(255), btree.Incl([]byte{1, 0x80, 0, 1}), btree.Excl([]byte{2})},
		{">= 10", executor.OpGE, types.Int32(10), btree.Incl(enc(types.Int32(10))), btree.Excl([]byte{2})},
		{"<= 'b'", executor.OpLE, types.Text("b"), btree.NoBound(), btree.Excl([]byte{1, 0x62, 0, 2})},
	}
	same := func(a, b btree.Bound) bool {
		return a.Unbounded == b.Unbounded && (a.Unbounded || (a.Inclusive == b.Inclusive && bytes.Equal(a.Key, b.Key)))
	}
	for _, tc := range tests {
		lo, hi, err := KeyBounds(tc.op, tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !same(lo, tc.lo) || !same(hi, tc.hi) {
			t.Fatalf("%s: [%+v, %+v], want [%+v, %+v]", tc.name, lo, hi, tc.lo, tc.hi)
		}
	}
	if _, _, err := KeyBounds(executor.OpNE, types.Int32(1)); !errors.Is(err, ErrBadOperator) {
		t.Fatalf("<>: err = %v, want ErrBadOperator", err)
	}
	if _, _, err := KeyBounds(executor.OpEQ, types.Null()); !errors.Is(err, ErrBadOperator) {
		t.Fatalf("= NULL: err = %v, want ErrBadOperator", err)
	}
}

// On a composite index the bounds must treat the leading value as a key prefix.
func TestP11_11_KeyBounds_CompositePrefix(t *testing.T) {
	db := newDB(t, Options{IndexFanout: 4})
	h := mustCreate(t, db, "emp", empSchema())
	tx := db.Begin(ReadCommitted)
	rawInsert(t, tx, h,
		R(int32(1), "alice", int32(10), 1), R(int32(2), "zed", int32(10), 1), R(int32(3), "a", int32(11), 1),
		R(int32(4), "zz", int32(9), 1), R(int32(5), "nul", nil, 1), R(int32(6), "b", int32(255), 1), R(int32(7), "c", int32(256), 1))
	if err := db.CreateIndex("emp_dn", "emp", []string{"dept", "name"}); err != nil {
		t.Fatal(err)
	}
	ix := db.IndexByName("emp_dn")
	tests := []struct {
		op    executor.CmpOp
		v     int32
		names string
	}{
		{executor.OpEQ, 10, "alice zed"},
		{executor.OpLT, 10, "zz"},
		{executor.OpLE, 10, "zz alice zed"},
		{executor.OpGT, 10, "a b c"},
		{executor.OpGE, 11, "a b c"},
		{executor.OpGT, 255, "c"},
		{executor.OpEQ, 255, "b"},
	}
	for _, tc := range tests {
		lo, hi, err := KeyBounds(tc.op, types.Int32(tc.v))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		cur := ix.Tree.Scan(lo, hi, false)
		for it, ok := cur.Next(); ok; it, ok = cur.Next() {
			r, _ := tuple.DeformTuple(ix.PayloadSchema, it.Payload)
			got = append(got, r[1].S)
		}
		if strings.Join(got, " ") != tc.names {
			t.Fatalf("dept %s %d → %v, want %s", tc.op, tc.v, got, tc.names)
		}
	}
}

func TestP11_12_IndexScan_RechecksVisibility(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	for _, r := range []types.Row{R(1, "alice", 10, 100), R(2, "bob", 20, 80), R(3, "carol", 10, 120), R(4, "dave", 20, 90), R(5, "erin", 30, 70), R(6, "frank", nil, 60)} {
		if _, err := tx.Insert("emp", r); err != nil {
			t.Fatal(err)
		}
	}
	commitRaw(t, tx)
	del := beginXid(db, ReadCommitted)
	stampXmax(t, h, types.TID{Page: 0, Slot: 3}, del.XID()) // carol deleted
	commitRaw(t, del)
	loser := db.Begin(ReadCommitted)
	if _, err := loser.Insert("emp", R(7, "gus", 10, 1)); err != nil { // aborted insert
		t.Fatal(err)
	}
	abortRaw(t, loser)

	ix := db.IndexByName("emp_dept")
	scan := func(op executor.CmpOp, v int32, emit bool) ([]types.Row, *IndexScan) {
		lo, hi, _ := KeyBounds(op, types.Int32(v))
		s := &IndexScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog, EmitTID: emit}
		rows, err := exec.Collect(s)
		if err != nil {
			t.Fatal(err)
		}
		return rows, s
	}
	rows, s := scan(executor.OpEQ, 10, false)
	testkit.AssertRows(t, rows, []types.Row{R(int32(1), "alice", int32(10), 100)})
	if s.HeapFetches != 3 {
		t.Fatalf("HeapFetches = %d, want 3 (alice, carol, gus)", s.HeapFetches)
	}
	rows, _ = scan(executor.OpGE, 20, false)
	testkit.AssertRows(t, rows, []types.Row{R(int32(2), "bob", int32(20), 80), R(int32(4), "dave", int32(20), 90), R(int32(5), "erin", int32(30), 70)})
	rows, _ = scan(executor.OpEQ, 20, true)
	testkit.AssertRows(t, rows, []types.Row{R(int32(2), "bob", int32(20), 80, 2), R(int32(4), "dave", int32(20), 90, 4)})

	fresh := &IndexScan{Index: ix}
	if _, _, err := fresh.Next(); !errors.Is(err, exec.ErrNotOpen) {
		t.Fatalf("Next before Open: err = %v, want exec.ErrNotOpen", err)
	}
}

// Every operator and value against an in-memory oracle over 50 employees.
func TestP11_12_IndexScan_MatchesOracle(t *testing.T) {
	db := newDB(t, Options{IndexFanout: 4})
	mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	for _, r := range emp50() {
		if _, err := tx.Insert("emp", r); err != nil {
			t.Fatal(err)
		}
	}
	commitRaw(t, tx)
	ix := db.IndexByName("emp_dept")
	for _, op := range []string{"=", "<", "<=", ">", ">="} {
		for _, v := range []int32{5, 10, 25, 30, 60, 70} {
			lo, hi, _ := KeyBounds(cmpOps[op], types.Int32(v))
			got, err := exec.Collect(&IndexScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog})
			if err != nil {
				t.Fatal(err)
			}
			want := filterRows(emp50(), func(r types.Row) bool { return cmpOp(r[2], op, types.Int32(v)) })
			testkit.AssertRows(t, got, sortRows(want, 2))
		}
	}
}

func wideSchema() types.Schema {
	return types.NewSchema("id", types.KindInt32, "name", types.KindText, "dept", types.KindInt32, "pad", types.KindText)
}

func TestP11_13_IndexOnlyScan_VisibilityMap(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "wide", wideSchema())
	if err := db.CreateIndex("wide_dept", "wide", []string{"dept"}, Include("name")); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("p", 3000) // two rows per page
	tx := db.Begin(ReadCommitted)
	var tids []types.TID
	for _, r := range []types.Row{R(1, "alice", 10, pad), R(2, "bob", 20, pad), R(3, "carol", 10, pad), R(4, "dave", 10, pad)} {
		tid, err := tx.Insert("wide", r)
		if err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
	}
	if tids[2] != (types.TID{Page: 1, Slot: 1}) || tids[3] != (types.TID{Page: 1, Slot: 2}) {
		t.Fatalf("placement = %v, want carol (1,1), dave (1,2)", tids)
	}
	commitRaw(t, tx)
	del := beginXid(db, ReadCommitted)
	stampXmax(t, h, tids[2], del.XID())
	commitRaw(t, del)

	ix := db.IndexByName("wide_dept")
	lo, hi, _ := KeyBounds(executor.OpEQ, types.Int32(10))
	run := func() ([]types.Row, int) {
		s := &IndexOnlyScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog}
		rows, err := exec.Collect(s)
		if err != nil {
			t.Fatal(err)
		}
		return rows, s.HeapFetches
	}

	h.vm = map[types.PageID]bool{0: true}
	rows, fetches := run()
	testkit.AssertRows(t, rows, []types.Row{R(int32(10), "alice"), R(int32(10), "dave")})
	if fetches != 2 {
		t.Fatalf("HeapFetches = %d, want 2 (carol and dave on page 1)", fetches)
	}

	h.vm = map[types.PageID]bool{}
	rows, fetches = run()
	testkit.AssertRows(t, rows, []types.Row{R(int32(10), "alice"), R(int32(10), "dave")})
	if fetches != 3 {
		t.Fatalf("empty VM: HeapFetches = %d, want 3", fetches)
	}

	// The VM is trusted blindly: a (wrong) all-visible bit hides carol's deletion.
	h.vm = map[types.PageID]bool{0: true, 1: true}
	rows, fetches = run()
	testkit.AssertRows(t, rows, []types.Row{R(int32(10), "alice"), R(int32(10), "carol"), R(int32(10), "dave")})
	if fetches != 0 {
		t.Fatalf("all-visible: HeapFetches = %d, want 0", fetches)
	}
	if _, _, err := (&IndexOnlyScan{Index: ix}).Next(); !errors.Is(err, exec.ErrNotOpen) {
		t.Fatalf("Next before Open: err = %v, want exec.ErrNotOpen", err)
	}
}

func TestP11_13_IndexOnlyScan_OwnUncommittedRows(t *testing.T) {
	db := newDB(t, Options{})
	mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}, Include("name")); err != nil {
		t.Fatal(err)
	}
	w := db.Begin(ReadCommitted)
	w.Insert("emp", R(1, "alice", 10, 1))
	ix := db.IndexByName("emp_dept")
	lo, hi, _ := KeyBounds(executor.OpEQ, types.Int32(10))
	mineRows, _ := exec.Collect(&IndexOnlyScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(w.XID()), Clog: db.mgr.Clog, Mine: w.mine()})
	others, _ := exec.Collect(&IndexOnlyScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog})
	if len(mineRows) != 1 || len(others) != 0 {
		t.Fatalf("writer sees %d rows, others %d; want 1 and 0", len(mineRows), len(others))
	}
}

func TestP11_9_Build_ManyPages(t *testing.T) {
	db := newDB(t, Options{PoolFrames: 4})
	h := mustCreate(t, db, "events", eventsSchema())
	tx := db.Begin(ReadCommitted)
	rawInsert(t, tx, h, events5000()...)
	if err := db.CreateIndex("events_kind", "events", []string{"kind", "amount"}, Include("id")); err != nil {
		t.Fatal(err)
	}
	ix := db.IndexByName("events_kind")
	if err := ix.Tree.Validate(); err != nil {
		t.Fatal(err)
	}
	items, _ := leafEntries(ix.Tree)
	if len(items) != 5000 {
		t.Fatalf("%d entries, want 5000", len(items))
	}
	for i := 1; i < len(items); i++ {
		if btree.CompareItems(items[i-1], items[i]) >= 0 {
			t.Fatalf("entries %d and %d out of (key, TID) order", i-1, i)
		}
	}
	// spot-check one entry against the heap
	row, _ := tuple.DeformTuple(ix.PayloadSchema, items[2500].Payload)
	stored, _ := tuple.DeformTuple(eventsSchema(), rawTuple(t, h, items[2500].TID))
	testkit.AssertRows(t, []types.Row{row}, []types.Row{{stored[1], stored[2], stored[0]}})
	for _, id := range h.pool.Resident() {
		if pins, _ := h.pool.PinCount(id); pins != 0 {
			t.Fatalf("Build left page %d pinned", id)
		}
	}
}

func TestP11_10_Insert_NullKeysAndNoIndexes(t *testing.T) {
	db := newDB(t, Options{})
	mustCreate(t, db, "emp", empSchema())
	mustCreate(t, db, "dept", deptSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	var tids []types.TID
	for _, r := range []types.Row{R(1, "a", nil, 1), R(2, "b", nil, 2), R(3, "c", 10, 3)} {
		tid, err := tx.Insert("emp", r)
		if err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
	}
	if got := db.IndexByName("emp_dept").Tree.Search([]byte{tuple.KeyTagNull}); !slices.Equal(got, tids[:2]) {
		t.Fatalf("NULL-key entries = %v, want %v (NULLs are indexed too)", got, tids[:2])
	}
	if tid, err := tx.Insert("dept", R(10, "eng")); err != nil || tid != (types.TID{Page: 0, Slot: 1}) {
		t.Fatalf("insert into a table without indexes = %v, %v", tid, err)
	}
	if tx.XID() != 3 {
		t.Fatalf("xid = %d, want 3 (one xid for the whole transaction)", tx.XID())
	}
}

func TestP11_11_KeyBounds_TextAndNegative(t *testing.T) {
	db := newDB(t, Options{IndexFanout: 4})
	h := mustCreate(t, db, "t", types.NewSchema("name", types.KindText, "n", types.KindInt64))
	tx := db.Begin(ReadCommitted)
	rawInsert(t, tx, h, R("carol", -5), R("carola", -1), R("car", 0), R("carol", 7), R(nil, 3), R("d", -100))
	if err := db.CreateIndex("t_name", "t", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateIndex("t_n", "t", []string{"n"}); err != nil {
		t.Fatal(err)
	}
	count := func(ix string, op executor.CmpOp, v types.Value) int {
		lo, hi, err := KeyBounds(op, v)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		cur := db.IndexByName(ix).Tree.Scan(lo, hi, false)
		for _, ok := cur.Next(); ok; _, ok = cur.Next() {
			n++
		}
		return n
	}
	tests := []struct {
		ix   string
		op   executor.CmpOp
		v    types.Value
		want int
	}{
		{"t_name", executor.OpEQ, types.Text("carol"), 2}, // not 'carola', not 'car'
		{"t_name", executor.OpGT, types.Text("carol"), 2}, // carola, d
		{"t_name", executor.OpLT, types.Text("carol"), 1}, // car
		{"t_name", executor.OpGE, types.Text("car"), 5},   // everything but NULL
		{"t_n", executor.OpLT, types.Int64(0), 3},         // -100, -5, -1
		{"t_n", executor.OpLE, types.Int64(-5), 2},        // -100, -5
		{"t_n", executor.OpGT, types.Int64(-1), 3},        // 0, 3, 7
	}
	for _, tc := range tests {
		if got := count(tc.ix, tc.op, tc.v); got != tc.want {
			t.Fatalf("%s %s %v: %d entries, want %d", tc.ix, tc.op, tc.v, got, tc.want)
		}
	}
}

func TestP11_12_IndexScan_OwnRowsPinsAndEmptyRange(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}); err != nil {
		t.Fatal(err)
	}
	w := db.Begin(ReadCommitted)
	w.Insert("emp", R(1, "alice", 10, 100))
	w.Insert("emp", R(2, "bob", 10, 80))
	ix := db.IndexByName("emp_dept")
	lo, hi, _ := KeyBounds(executor.OpEQ, types.Int32(10))
	mine, _ := exec.Collect(&IndexScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(w.XID()), Clog: db.mgr.Clog, Mine: w.mine()})
	if len(mine) != 2 {
		t.Fatalf("the writer sees %d of its own rows, want 2", len(mine))
	}
	others, _ := exec.Collect(&IndexScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog})
	if len(others) != 0 {
		t.Fatalf("another transaction sees %d uncommitted rows, want 0", len(others))
	}
	lo, hi, _ = KeyBounds(executor.OpGT, types.Int32(10))
	s := &IndexScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog}
	if rows, _ := exec.Collect(s); len(rows) != 0 || s.HeapFetches != 0 {
		t.Fatalf("empty range: %d rows, %d heap fetches; want 0, 0", len(rows), s.HeapFetches)
	}
	if pins, _ := h.pool.PinCount(0); pins != 0 {
		t.Fatalf("page 0 pin count = %d after the scans, want 0", pins)
	}
}

// An index entry whose heap slot no longer holds a tuple is skipped.
func TestP11_13_IndexOnlyScan_SkipsDeadSlots(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}, Include("name")); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	tx.Insert("emp", R(1, "alice", 10, 1))
	tx.Insert("emp", R(2, "bob", 10, 1))
	commitRaw(t, tx)
	f, _ := h.pool.FetchPage(0)
	if err := heapPage(f).DeleteTuple(1); err != nil { // line pointer of alice → DEAD
		t.Fatal(err)
	}
	h.pool.UnpinPage(0, true)
	ix := db.IndexByName("emp_dept")
	lo, hi, _ := KeyBounds(executor.OpEQ, types.Int32(10))
	s := &IndexOnlyScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog}
	rows, err := exec.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, rows, []types.Row{R(int32(10), "bob")})
	if s.HeapFetches != 2 {
		t.Fatalf("HeapFetches = %d, want 2", s.HeapFetches)
	}
	is := &IndexScan{Index: ix, Lo: lo, Hi: hi, Snap: db.mgr.GetSnapshot(0), Clog: db.mgr.Clog}
	rows, _ = exec.Collect(is)
	testkit.AssertRows(t, rows, []types.Row{R(int32(2), "bob", int32(10), 1)})
}
