package txn

import (
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func tid(page, slot int) types.TID {
	return types.TID{Page: types.PageID(page), Slot: types.SlotID(slot)}
}

// clogWith builds a clog from xid → status.
func clogWith(t *testing.T, st map[types.TxnID]TxnStatus) *Clog {
	t.Helper()
	c := NewClog()
	for x, s := range st {
		if err := c.Set(x, s); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestP9_8_TupleVisible(t *testing.T) {
	// The world as seen by my transaction 6 (with live subxact 8):
	//   3  committed long ago           4  aborted
	//   5  running (in Xip)             7  my subxact, rolled back
	//   9  running at snapshot time, committed since (in Xip)
	//   10 started after the snapshot, committed
	clog := clogWith(t, map[types.TxnID]TxnStatus{
		3: StatusCommitted, 4: StatusAborted, 5: StatusInProgress, 6: StatusInProgress,
		7: StatusAborted, 8: StatusInProgress, 9: StatusCommitted, 10: StatusCommitted,
	})
	snap := &Snapshot{Xmin: 5, Xmax: 10, Xip: []types.TxnID{5, 9}, CurCid: 2}
	mine := CurrentXids{6, 8}

	tests := []struct {
		name      string
		v         Version
		want      bool
		wantHints InfoMask
	}{
		// --- inserter
		{"committed insert", Version{Xmin: 3}, true, XminCommitted},
		{"aborted insert", Version{Xmin: 4}, false, XminInvalid},
		{"insert by running txn", Version{Xmin: 5}, false, 0},
		{"insert committed after snapshot (in Xip) — clog not consulted", Version{Xmin: 9}, false, 0},
		{"insert by txn that started after snapshot", Version{Xmin: 10}, false, 0},
		{"invalid xmin", Version{Xmin: types.InvalidTxnID}, false, XminInvalid},
		{"frozen xid 2 as xmin", Version{Xmin: FrozenTxnID}, true, XminCommitted},
		// --- my own inserts (command ids)
		{"my insert from an earlier statement", Version{Xmin: 6, Cmin: 1}, true, 0},
		{"my insert from this statement", Version{Xmin: 6, Cmin: 2}, false, 0},
		{"my insert from a later command", Version{Xmin: 6, Cmin: 3}, false, 0},
		{"insert by my live subxact", Version{Xmin: 8, Cmin: 0}, true, 0},
		{"insert by my rolled-back subxact", Version{Xmin: 7, Cmin: 0}, false, XminInvalid},
		{"my insert, I deleted it in an earlier statement", Version{Xmin: 6, Cmin: 0, Xmax: 6, Cmax: 1}, false, 0},
		{"my insert, I am deleting it in this statement", Version{Xmin: 6, Cmin: 0, Xmax: 8, Cmax: 2}, true, 0},
		{"my insert, deleted by my rolled-back subxact", Version{Xmin: 6, Cmin: 0, Xmax: 7, Cmax: 1}, true, 0},
		// --- deleter
		{"deleted by aborted txn", Version{Xmin: 3, Xmax: 4}, true, XminCommitted | XmaxInvalid},
		{"deleted by running txn", Version{Xmin: 3, Xmax: 5}, true, XminCommitted},
		{"deleted by txn committed after snapshot", Version{Xmin: 3, Xmax: 9}, true, XminCommitted},
		{"deleted by txn started after snapshot", Version{Xmin: 3, Xmax: 10}, true, XminCommitted},
		{"deleted by committed txn", Version{Xmin: 3, Xmax: 3}, false, XminCommitted | XmaxCommitted},
		{"I deleted it in an earlier statement", Version{Xmin: 3, Xmax: 6, Cmax: 1}, false, XminCommitted},
		{"I am deleting it in this statement", Version{Xmin: 3, Xmax: 6, Cmax: 2}, true, XminCommitted},
		{"deleted by my rolled-back subxact", Version{Xmin: 3, Xmax: 7, Cmax: 0}, true, XminCommitted | XmaxInvalid},
		// --- hint bits
		{"XminCommitted hint still checks the snapshot", Version{Xmin: 9, Hints: XminCommitted}, false, XminCommitted},
		{"XminCommitted hint is trusted over clog", Version{Xmin: 4, Hints: XminCommitted}, true, XminCommitted},
		{"XminInvalid hint is trusted over clog", Version{Xmin: 3, Hints: XminInvalid}, false, XminInvalid},
		{"frozen ignores the snapshot", Version{Xmin: 10, Hints: XminFrozen}, true, XminFrozen},
		{"frozen but deleted by committed txn", Version{Xmin: 10, Xmax: 3, Hints: XminFrozen}, false, XminFrozen | XmaxCommitted},
		{"XmaxCommitted hint, deleter in snapshot", Version{Xmin: 3, Xmax: 9, Hints: XminCommitted | XmaxCommitted}, true, XminCommitted | XmaxCommitted},
		{"XmaxInvalid hint is trusted over clog", Version{Xmin: 3, Xmax: 3, Hints: XminCommitted | XmaxInvalid}, true, XminCommitted | XmaxInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			if got := TupleVisible(&v, snap, clog, mine); got != tc.want {
				t.Fatalf("visible = %v, want %v", got, tc.want)
			}
			if v.Hints != tc.wantHints {
				t.Fatalf("hints = %04b, want %04b", v.Hints, tc.wantHints)
			}
			if v.Xmin != tc.v.Xmin || v.Xmax != tc.v.Xmax || v.Cmin != tc.v.Cmin || v.Cmax != tc.v.Cmax {
				t.Fatalf("TupleVisible modified header fields other than Hints")
			}
		})
	}
}

func TestP9_8_TupleVisible_DocExample(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted, 4: StatusAborted})
	snap := &Snapshot{Xmin: 5, Xmax: 7, Xip: []types.TxnID{5}, CurCid: 1}
	mine := CurrentXids{6}
	tests := []struct {
		v         Version
		want      bool
		wantHints InfoMask
	}{
		{Version{Xmin: 3}, true, XminCommitted},
		{Version{Xmin: 4}, false, XminInvalid},
		{Version{Xmin: 5}, false, 0},
		{Version{Xmin: 3, Xmax: 4}, true, XminCommitted | XmaxInvalid},
		{Version{Xmin: 6, Cmin: 0}, true, 0},
		{Version{Xmin: 6, Cmin: 1}, false, 0},
		{Version{Xmin: 6, Cmin: 0, Xmax: 6, Cmax: 0}, false, 0},
	}
	for i, tc := range tests {
		v := tc.v
		if got := TupleVisible(&v, snap, clog, mine); got != tc.want || v.Hints != tc.wantHints {
			t.Errorf("case %d %+v: visible=%v hints=%04b, want %v %04b", i, tc.v, got, v.Hints, tc.want, tc.wantHints)
		}
	}
}

func TestP9_8_TupleVisible_ReadOnlySnapshot(t *testing.T) {
	// A transaction without an xid (mine == nil) must still see committed data
	// and must never mistake xid 0 for itself.
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted})
	snap := &Snapshot{Xmin: 4, Xmax: 4}
	if !TupleVisible(&Version{Xmin: 3}, snap, clog, nil) {
		t.Fatal("committed row invisible to read-only snapshot")
	}
	if TupleVisible(&Version{Xmin: 4}, snap, clog, nil) {
		t.Fatal("row from xid >= Xmax visible")
	}
	if !TupleVisible(&Version{Xmin: 3, Xmax: types.InvalidTxnID}, snap, clog, CurrentXids{}) {
		t.Fatal("live row invisible with empty CurrentXids")
	}
}

func TestP9_9_Insert(t *testing.T) {
	h := &Heap{}
	rows := testkit.Rows([]any{1, "a"}, []any{2, "b"}, []any{3, "c"}, []any{4, "d"}, []any{5, "e"})
	wantTIDs := []types.TID{tid(0, 1), tid(0, 2), tid(0, 3), tid(0, 4), tid(1, 1)}
	for i, r := range rows {
		xid, cid := types.TxnID(10), CommandID(i)
		if i == 4 {
			xid, cid = 11, 0
		}
		got := h.Insert(xid, cid, r)
		if got != wantTIDs[i] {
			t.Fatalf("Insert #%d → %v, want %v", i, got, wantTIDs[i])
		}
		v := h.Get(got)
		want := Version{Xmin: xid, Cmin: cid, Ctid: got}
		if v.Xmin != want.Xmin || v.Cmin != want.Cmin || v.Xmax != 0 || v.Cmax != 0 || v.Hints != 0 || v.Ctid != got {
			t.Fatalf("version at %v = %+v, want Xmin=%d Cmin=%d Xmax=0 Cmax=0 Hints=0 Ctid=%v", got, *v, xid, cid, got)
		}
	}
	if len(h.Versions) != 5 {
		t.Fatalf("len(Versions) = %d, want 5", len(h.Versions))
	}
	testkit.AssertRows(t, h.Rows(wantTIDs), rows)
}

// oneVersion returns a heap whose (0,1) is v (Ctid defaults to (0,1)) plus an
// unrelated second version at (0,2).
func oneVersion(v Version) *Heap {
	if v.Ctid == (types.TID{}) {
		v.Ctid = tid(0, 1)
	}
	v.Data = testkit.R(1, "a")
	return &Heap{Versions: []*Version{&v, {Xmin: 3, Ctid: tid(0, 2), Data: testkit.R(2, "b")}}}
}

func TestP9_10_Delete(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted, 4: StatusAborted, 5: StatusInProgress, 7: StatusInProgress})
	mine := CurrentXids{7}
	tests := []struct {
		name string
		v    Version
		cid  CommandID
		want TMResult
	}{
		{"live row", Version{Xmin: 3}, 0, TMOk},
		{"previous deleter aborted", Version{Xmin: 3, Xmax: 4}, 0, TMOk},
		{"XmaxInvalid hint is cleared on success", Version{Xmin: 3, Xmax: 4, Hints: XminCommitted | XmaxInvalid}, 0, TMOk},
		{"deleter still running", Version{Xmin: 3, Xmax: 5}, 0, TMBeingModified},
		{"deleted by committed txn", Version{Xmin: 3, Xmax: 3, Ctid: tid(0, 1)}, 0, TMDeleted},
		{"updated by committed txn", Version{Xmin: 3, Xmax: 3, Ctid: tid(0, 2)}, 0, TMUpdated},
		{"XmaxCommitted hint trusted (clog says running)", Version{Xmin: 3, Xmax: 5, Hints: XmaxCommitted}, 0, TMDeleted},
		{"already deleted by me", Version{Xmin: 3, Xmax: 7, Cmax: 0}, 1, TMSelfModified},
		{"inserter still running", Version{Xmin: 5}, 0, TMInvisible},
		{"inserter aborted", Version{Xmin: 4}, 0, TMInvisible},
		{"frozen hint trusted (clog says aborted)", Version{Xmin: 4, Hints: XminFrozen}, 0, TMOk},
		{"my insert from an earlier command", Version{Xmin: 7, Cmin: 0}, 1, TMOk},
		{"my insert from this command", Version{Xmin: 7, Cmin: 1}, 1, TMInvisible},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := oneVersion(tc.v)
			before := *h.Versions[0]
			if got := h.Delete(tid(0, 1), 7, tc.cid, clog, mine); got != tc.want {
				t.Fatalf("Delete = %v, want %v", got, tc.want)
			}
			after := *h.Versions[0]
			if tc.want != TMOk {
				if after.Xmax != before.Xmax || after.Cmax != before.Cmax || after.Hints != before.Hints || after.Ctid != before.Ctid {
					t.Fatalf("failed Delete changed the version: %+v → %+v", before, after)
				}
				return
			}
			if after.Xmax != 7 || after.Cmax != tc.cid {
				t.Fatalf("Xmax/Cmax = %d/%d, want 7/%d", after.Xmax, after.Cmax, tc.cid)
			}
			if after.Hints&(XmaxCommitted|XmaxInvalid) != 0 {
				t.Fatalf("xmax hints not cleared: %04b", after.Hints)
			}
			if after.Hints != before.Hints&^(XmaxCommitted|XmaxInvalid) {
				t.Fatalf("xmin hints changed: %04b → %04b", before.Hints, after.Hints)
			}
			if after.Ctid != before.Ctid || after.Xmin != before.Xmin {
				t.Fatalf("Delete changed Ctid or Xmin")
			}
			if len(h.Versions) != 2 {
				t.Fatalf("Delete appended a version")
			}
		})
	}
}

func TestP9_10_Delete_MissingVersion(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted})
	h := &Heap{Versions: []*Version{nil, {Xmin: 3, Ctid: tid(0, 2)}}}
	for _, target := range []types.TID{tid(0, 1), tid(0, 3), tid(5, 1), tid(0, 0)} {
		if got := h.Delete(target, 7, 0, clog, CurrentXids{7}); got != TMInvisible {
			t.Errorf("Delete(%v) = %v, want TM_Invisible", target, got)
		}
	}
}

func TestP9_11_Update(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted})
	h := &Heap{}
	h.Insert(3, 0, testkit.R(1, "a"))
	newTID, res := h.Update(tid(0, 1), 7, 0, testkit.R(1, "b"), clog, CurrentXids{7})
	if res != TMOk || newTID != tid(0, 2) {
		t.Fatalf("Update = (%v, %v), want ((0,2), TM_Ok)", newTID, res)
	}
	old, nv := h.Get(tid(0, 1)), h.Get(tid(0, 2))
	if old.Xmax != 7 || old.Cmax != 0 || old.Ctid != tid(0, 2) {
		t.Fatalf("old version = %+v, want Xmax=7 Cmax=0 Ctid=(0,2)", *old)
	}
	testkit.AssertRows(t, []types.Row{old.Data}, []types.Row{testkit.R(1, "a")})
	if nv.Xmin != 7 || nv.Cmin != 0 || nv.Xmax != 0 || nv.Ctid != tid(0, 2) || nv.Hints != 0 {
		t.Fatalf("new version = %+v, want Xmin=7 Cmin=0 Xmax=0 Ctid=(0,2)", *nv)
	}
	testkit.AssertRows(t, []types.Row{nv.Data}, []types.Row{testkit.R(1, "b")})
}

func TestP9_11_Update_ChainAcrossPages(t *testing.T) {
	// T3 inserts; T4, T5, T6, T7 each update the newest version and commit.
	m := NewTxnManager()
	h := &Heap{}
	t3 := m.Begin()
	h.Insert(t3, 0, testkit.R(1, 0))
	m.Commit(t3)
	cur := tid(0, 1)
	for i := 1; i <= 4; i++ {
		x := m.Begin()
		next, res := h.Update(cur, x, 0, testkit.R(1, i), m.Clog, CurrentXids{x})
		if res != TMOk {
			t.Fatalf("update #%d = %v", i, res)
		}
		m.Commit(x)
		cur = next
	}
	// Follow the chain from the root.
	want := []types.TID{tid(0, 1), tid(0, 2), tid(0, 3), tid(0, 4), tid(1, 1)}
	got := []types.TID{tid(0, 1)}
	for v := h.Get(got[0]); v.Ctid != got[len(got)-1]; v = h.Get(v.Ctid) {
		got = append(got, v.Ctid)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ctid chain = %v, want %v", got, want)
	}
	if v := h.Get(tid(1, 1)); v.Xmin != 7 || v.Data[1].I != 4 {
		t.Fatalf("newest version = %+v, want Xmin=7 value 4", *v)
	}
}

func TestP9_11_Update_FailureLeavesHeapUnchanged(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted, 5: StatusInProgress})
	tests := []struct {
		name string
		v    Version
		want TMResult
	}{
		{"row locked by running txn", Version{Xmin: 3, Xmax: 5}, TMBeingModified},
		{"row already updated by committed txn", Version{Xmin: 3, Xmax: 3, Ctid: tid(0, 2)}, TMUpdated},
		{"row deleted by committed txn", Version{Xmin: 3, Xmax: 3}, TMDeleted},
		{"row I already updated", Version{Xmin: 3, Xmax: 7}, TMSelfModified},
		{"row inserted by running txn", Version{Xmin: 5}, TMInvisible},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := oneVersion(tc.v)
			before := *h.Versions[0]
			newTID, res := h.Update(tid(0, 1), 7, 1, testkit.R(1, "z"), clog, CurrentXids{7})
			if res != tc.want || newTID != (types.TID{}) {
				t.Fatalf("Update = (%v, %v), want ((0,0), %v)", newTID, res, tc.want)
			}
			if len(h.Versions) != 2 {
				t.Fatalf("failed Update appended a version (len %d)", len(h.Versions))
			}
			if a := h.Versions[0]; a.Xmax != before.Xmax || a.Cmax != before.Cmax || a.Ctid != before.Ctid || a.Hints != before.Hints {
				t.Fatalf("failed Update changed the old version")
			}
		})
	}
}

func snapAt(m *TxnManager, xid types.TxnID, cid CommandID) *Snapshot {
	s := m.GetSnapshot(xid)
	s.CurCid = cid
	return s
}

func TestP9_12_Scan_RollbackLeavesDataButHidesIt(t *testing.T) {
	m := NewTxnManager()
	h := &Heap{}
	t3 := m.Begin()
	h.Insert(t3, 0, testkit.R(1, "a"))
	m.Commit(t3)

	t4 := m.Begin()
	h.Update(tid(0, 1), t4, 0, testkit.R(1, "b"), m.Clog, CurrentXids{t4})
	// T4 itself sees its update in its next statement.
	testkit.AssertRows(t, h.Rows(h.Scan(snapAt(m, t4, 1), m.Clog, CurrentXids{t4})), []types.Row{testkit.R(1, "b")})
	// Others still see the old value.
	testkit.AssertRows(t, h.Rows(h.Scan(snapAt(m, 0, 0), m.Clog, nil)), []types.Row{testkit.R(1, "a")})

	m.Abort(t4)
	got := h.Scan(snapAt(m, 0, 0), m.Clog, nil)
	if !slices.Equal(got, []types.TID{tid(0, 1)}) {
		t.Fatalf("Scan after rollback = %v, want [(0,1)]", got)
	}
	// Nothing was undone physically: both versions are still there.
	if len(h.Versions) != 2 || h.Versions[1].Data[1].S != "b" || h.Versions[0].Xmax != t4 {
		t.Fatalf("rollback must not touch the heap; versions = %+v %+v", *h.Versions[0], *h.Versions[1])
	}
	// The scan left hint bits behind.
	if h.Versions[0].Hints != XminCommitted|XmaxInvalid {
		t.Fatalf("(0,1) hints = %04b, want XminCommitted|XmaxInvalid", h.Versions[0].Hints)
	}
	if h.Versions[1].Hints != XminInvalid {
		t.Fatalf("(0,2) hints = %04b, want XminInvalid", h.Versions[1].Hints)
	}
}

func TestP9_12_Scan_CommitVisibleOnlyToLaterSnapshots(t *testing.T) {
	m := NewTxnManager()
	h := &Heap{}
	t3, t4 := m.Begin(), m.Begin()
	h.Insert(t3, 0, testkit.R(1))
	h.Insert(t3, 0, testkit.R(2))

	before := snapAt(m, t4, 0) // taken while T3 is running
	if got := h.Scan(before, m.Clog, CurrentXids{t4}); len(got) != 0 {
		t.Fatalf("T4 sees uncommitted rows %v", got)
	}
	if got := h.Scan(snapAt(m, t3, 0), m.Clog, CurrentXids{t3}); len(got) != 0 {
		t.Fatalf("T3 sees its own rows inside the inserting statement: %v", got)
	}
	if got := h.Scan(snapAt(m, t3, 1), m.Clog, CurrentXids{t3}); !slices.Equal(got, []types.TID{tid(0, 1), tid(0, 2)}) {
		t.Fatalf("T3 next statement = %v, want [(0,1) (0,2)]", got)
	}

	m.Commit(t3)
	if got := h.Scan(before, m.Clog, CurrentXids{t4}); len(got) != 0 {
		t.Fatalf("old snapshot sees rows committed after it: %v", got)
	}
	testkit.AssertRows(t, h.Rows(h.Scan(snapAt(m, t4, 1), m.Clog, CurrentXids{t4})), testkit.Rows([]any{1}, []any{2}))
}

func TestP9_12_Scan_DeleteCommitAndAbort(t *testing.T) {
	m := NewTxnManager()
	h := &Heap{}
	t3 := m.Begin()
	h.Insert(t3, 0, testkit.R(1))
	h.Insert(t3, 0, testkit.R(2))
	h.Insert(t3, 0, testkit.R(3))
	m.Commit(t3)

	t4 := m.Begin()
	h.Delete(tid(0, 1), t4, 0, m.Clog, CurrentXids{t4})
	m.Abort(t4) // aborted delete: row 1 survives

	t5 := m.Begin()
	h.Delete(tid(0, 2), t5, 0, m.Clog, CurrentXids{t5})
	old := snapAt(m, 0, 0) // taken while T5 is running
	m.Commit(t5)           // committed delete: row 2 disappears for new snapshots

	testkit.AssertRows(t, h.Rows(h.Scan(snapAt(m, 0, 0), m.Clog, nil)), testkit.Rows([]any{1}, []any{3}))
	testkit.AssertRows(t, h.Rows(h.Scan(old, m.Clog, nil)), testkit.Rows([]any{1}, []any{2}, []any{3}))
}

func TestP9_12_Scan_SkipsRemovedAndEmpty(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted})
	snap := &Snapshot{Xmin: 4, Xmax: 4}
	empty := &Heap{}
	if got := empty.Scan(snap, clog, nil); got == nil || len(got) != 0 {
		t.Fatalf("Scan(empty) = %#v, want empty non-nil slice", got)
	}
	h := &Heap{Versions: []*Version{nil, {Xmin: 3, Ctid: tid(0, 2)}, nil, nil, {Xmin: 3, Ctid: tid(1, 1)}}}
	if got := h.Scan(snap, clog, nil); !slices.Equal(got, []types.TID{tid(0, 2), tid(1, 1)}) {
		t.Fatalf("Scan = %v, want [(0,2) (1,1)]", got)
	}
}
