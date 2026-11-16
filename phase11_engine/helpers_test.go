package engine

import (
	"math/rand/v2"
	"os"
	"slices"
	"testing"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// Test helpers. They call only provided code, so any test may use them.

var R = testkit.R

// empSchema: emp(id int4, name text, dept int4, salary int8).
func empSchema() types.Schema {
	return types.NewSchema("id", types.KindInt32, "name", types.KindText, "dept", types.KindInt32, "salary", types.KindInt64)
}

// deptSchema: dept(id int4, name text).
func deptSchema() types.Schema {
	return types.NewSchema("id", types.KindInt32, "name", types.KindText)
}

var empNames = []string{
	"alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi", "ivan", "judy",
	"kevin", "laura", "mallory", "nina", "oscar", "peggy", "quinn", "rupert", "sybil", "trent",
	"ursula", "victor", "wendy", "xavier", "yvonne", "zach", "amber", "blake", "cora", "dmitri",
	"elena", "felix", "gemma", "hugo", "iris", "jonah", "kira", "leon", "maya", "nolan",
	"olive", "pablo", "rosa", "silas", "tara", "uma", "vince", "willa", "yara", "zane",
}

// emp50 is the 50-employee dataset, rows typed exactly like empSchema:
// id i, name empNames[i-1], dept NULL when i%9 == 0 else [10 20 30 40 60][i%5]
// (there is no department 60), salary 50 + (37*i)%100.
func emp50() []types.Row {
	depts := []int32{10, 20, 30, 40, 60}
	var out []types.Row
	for i := 1; i <= 50; i++ {
		dept := types.Int32(depts[i%5])
		if i%9 == 0 {
			dept = types.Null()
		}
		out = append(out, types.Row{types.Int32(int32(i)), types.Text(empNames[i-1]), dept, types.Int64(int64(50 + (37*i)%100))})
	}
	return out
}

// dept5: departments 10..50; 50 'legal' has no employees.
func dept5() []types.Row {
	return []types.Row{
		{types.Int32(10), types.Text("eng")},
		{types.Int32(20), types.Text("sales")},
		{types.Int32(30), types.Text("ops")},
		{types.Int32(40), types.Text("hr")},
		{types.Int32(50), types.Text("legal")},
	}
}

// newDB opens a fresh database in a temp dir; the test cleanup crashes it
// (Crash calls no problem code, unlike Close).
func newDB(t testing.TB, opts Options) *DB {
	t.Helper()
	opts.NoSync = true
	db, err := OpenWith(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(db.Crash)
	return db
}

// reopen crashes db (if still open) and opens its directory again.
func reopen(t testing.TB, db *DB, opts Options) *DB {
	t.Helper()
	db.Crash()
	opts.NoSync = true
	db2, err := OpenWith(db.Dir(), opts)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(db2.Crash)
	return db2
}

func mustCreate(t testing.TB, db *DB, name string, s types.Schema) *TableHeap {
	t.Helper()
	if err := db.CreateTable(name, s); err != nil {
		t.Fatalf("CreateTable(%s): %v", name, err)
	}
	return db.Heap(name)
}

// beginXid starts a transaction and gives it an xid right away.
func beginXid(db *DB, level IsolationLevel) *Txn {
	t := db.Begin(level)
	t.assignXid()
	return t
}

// commitRaw / abortRaw end a transaction through the phase-9 manager only
// (no WAL record): for tests of problems that come before Commit/Rollback.
func commitRaw(t testing.TB, tx *Txn) {
	t.Helper()
	if tx.xid != 0 {
		if err := tx.db.mgr.Commit(tx.xid); err != nil {
			t.Fatalf("mgr.Commit(%d): %v", tx.xid, err)
		}
	}
	tx.finish()
}

func abortRaw(t testing.TB, tx *Txn) {
	t.Helper()
	if tx.xid != 0 {
		if err := tx.db.mgr.Abort(tx.xid); err != nil {
			t.Fatalf("mgr.Abort(%d): %v", tx.xid, err)
		}
	}
	tx.finish()
}

// rawInsert stores rows with TableHeap.Insert (Problem 11.4) under tx's xid.
func rawInsert(t testing.TB, tx *Txn, h *TableHeap, rows ...types.Row) []types.TID {
	t.Helper()
	xid := tx.assignXid()
	var tids []types.TID
	for _, r := range rows {
		tid, err := h.Insert(xid, r)
		if err != nil {
			t.Fatalf("Insert(%v): %v", r, err)
		}
		tids = append(tids, tid)
	}
	return tids
}

// stampXmax writes xmax into the stored tuple at tid with raw page access
// (no WAL), simulating a DELETE for tests that come before Problem 11.17.
func stampXmax(t testing.TB, h *TableHeap, tid types.TID, xmax types.TxnID) {
	t.Helper()
	f, err := h.pool.FetchPage(tid.Page)
	if err != nil {
		t.Fatal(err)
	}
	data, err := heapPage(f).GetTuple(tid.Slot)
	if err != nil {
		t.Fatalf("stampXmax %v: %v", tid, err)
	}
	setXmax(data, xmax)
	delete(h.vm, tid.Page)
	h.pool.UnpinPage(tid.Page, true)
}

// rawTuple returns a copy of the stored bytes at tid (nil if no live tuple).
func rawTuple(t testing.TB, h *TableHeap, tid types.TID) []byte {
	t.Helper()
	f, err := h.pool.FetchPage(tid.Page)
	if err != nil {
		t.Fatal(err)
	}
	defer h.pool.UnpinPage(tid.Page, false)
	b, err := heapPage(f).GetTuple(tid.Slot)
	if err != nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// rawHeader decodes the header stored at tid.
func rawHeader(t testing.TB, h *TableHeap, tid types.TID) tuple.TupleHeader {
	t.Helper()
	b := rawTuple(t, h, tid)
	if b == nil {
		t.Fatalf("no tuple at %v", tid)
	}
	hdr, err := tuple.DecodeHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	return hdr
}

// lsnOf returns the page LSN of a heap page.
func lsnOf(t testing.TB, h *TableHeap, pid types.PageID) types.LSN {
	t.Helper()
	f, err := h.pool.FetchPage(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer h.pool.UnpinPage(pid, false)
	return pageLSN(heapPage(f))
}

// walRecords decodes the whole log: flushed file contents + the WAL buffer.
func walRecords(t testing.TB, db *DB) []wal.Record {
	t.Helper()
	data, err := os.ReadFile(db.wal.f.Name())
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:db.wal.flushed], db.wal.buf...)
	recs, _, err := wal.ReadRecords(data, wal.FirstLSN)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// heapRecs keeps the RecUpdate records of a log, decoded.
func heapRecs(t testing.TB, recs []wal.Record) []heapRecord {
	t.Helper()
	var out []heapRecord
	for _, r := range recs {
		if r.Type != wal.RecUpdate {
			continue
		}
		hr, err := decodeHeapRecord(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, hr)
	}
	return out
}

// seqRows scans a table with a SeqScan (Problem 11.5) using a fresh RC
// snapshot taken through the phase-9 manager for tx.
func seqRows(t testing.TB, tx *Txn, h *TableHeap) []types.Row {
	t.Helper()
	s := &SeqScan{Heap: h, Snap: tx.db.mgr.GetSnapshot(tx.xid), Clog: tx.db.mgr.Clog, Mine: tx.mine()}
	rows, err := exec.Collect(s)
	if err != nil {
		t.Fatalf("SeqScan: %v", err)
	}
	return rows
}

// oracle helpers ------------------------------------------------------------

func filterRows(rows []types.Row, pred func(types.Row) bool) []types.Row {
	var out []types.Row
	for _, r := range rows {
		if pred(r) {
			out = append(out, r)
		}
	}
	return out
}

func project(rows []types.Row, cols ...int) []types.Row {
	out := make([]types.Row, len(rows))
	for i, r := range rows {
		for _, c := range cols {
			out[i] = append(out[i], r[c])
		}
	}
	return out
}

// cmpOp evaluates "v op c" with SQL semantics (NULL → false).
func cmpOp(v types.Value, op string, c types.Value) bool {
	if v.IsNull() || c.IsNull() {
		return false
	}
	x := types.Compare(v, c)
	switch op {
	case "=":
		return x == 0
	case "<>":
		return x != 0
	case "<":
		return x < 0
	case "<=":
		return x <= 0
	case ">":
		return x > 0
	case ">=":
		return x >= 0
	}
	panic(op)
}

func sortRows(rows []types.Row, col int) []types.Row {
	out := slices.Clone(rows)
	slices.SortStableFunc(out, func(a, b types.Row) int {
		x, y := a[col], b[col]
		switch {
		case x.IsNull() && y.IsNull():
			return 0
		case x.IsNull():
			return 1
		case y.IsNull():
			return -1
		}
		return types.Compare(x, y)
	})
	return out
}

// event5000 is the seeded 5000-row table: events(id int8, kind text, amount int8).
func eventsSchema() types.Schema {
	return types.NewSchema("id", types.KindInt64, "kind", types.KindText, "amount", types.KindInt64)
}

func events5000() []types.Row {
	r := rand.New(rand.NewPCG(11, 2026))
	kinds := []string{"click", "view", "purchase", "refund", "signup"}
	out := make([]types.Row, 5000)
	for i := range out {
		out[i] = types.Row{types.Int64(int64(i + 1)), types.Text(kinds[r.IntN(len(kinds))]), types.Int64(int64(r.IntN(1000)))}
	}
	return out
}

// mkTIDs builds TIDs from page, slot pairs.
func mkTIDs(ps ...int) []types.TID {
	var out []types.TID
	for i := 0; i < len(ps); i += 2 {
		out = append(out, types.TID{Page: types.PageID(ps[i]), Slot: types.SlotID(ps[i+1])})
	}
	return out
}
