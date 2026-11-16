package txn

import (
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP9_20_Vacuum_DocExample(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{3: StatusCommitted, 4: StatusCommitted, 5: StatusAborted, 6: StatusInProgress})
	h := &Heap{Versions: []*Version{
		{Xmin: 3, Xmax: 4, Ctid: tid(0, 2)},
		{Xmin: 4, Xmax: 5, Ctid: tid(0, 3)},
		{Xmin: 5, Ctid: tid(0, 3)},
		{Xmin: 3, Xmax: 6, Ctid: tid(0, 4)},
	}}
	if got := Vacuum(h, clog, 6); got != 2 {
		t.Fatalf("Vacuum = %d, want 2", got)
	}
	if h.Versions[0] != nil || h.Versions[2] != nil {
		t.Fatalf("(0,1) and (0,3) should be removed")
	}
	if v := h.Versions[1]; v.Hints != XminCommitted|XmaxInvalid || v.Ctid != tid(0, 2) {
		t.Fatalf("(0,2) = hints %04b ctid %v, want XminCommitted|XmaxInvalid, (0,2)", v.Hints, v.Ctid)
	}
	if v := h.Versions[3]; v.Hints != XminCommitted || v.Ctid != tid(0, 4) {
		t.Fatalf("(0,4) = hints %04b ctid %v, want XminCommitted, (0,4)", v.Hints, v.Ctid)
	}
	if got := Vacuum(h, clog, 6); got != 0 {
		t.Fatalf("second Vacuum = %d, want 0 (removed slots are not counted again)", got)
	}
}

func TestP9_20_Vacuum_Rules(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{
		10: StatusCommitted, 11: StatusAborted, 12: StatusInProgress, 19: StatusCommitted, 20: StatusCommitted,
	})
	const horizon = 20
	tests := []struct {
		name        string
		v           Version
		wantRemoved bool
		wantHints   InfoMask
	}{
		{"live committed row", Version{Xmin: 10}, false, XminCommitted},
		{"aborted insert", Version{Xmin: 11}, true, 0},
		{"aborted insert known by hint", Version{Xmin: 12, Hints: XminInvalid}, true, 0},
		{"insert still running", Version{Xmin: 12}, false, 0},
		{"deleted just before the horizon", Version{Xmin: 10, Xmax: 19}, true, 0},
		{"deleted exactly at the horizon: recently dead", Version{Xmin: 10, Xmax: 20}, false, XminCommitted | XmaxCommitted},
		{"deleted by aborted txn", Version{Xmin: 10, Xmax: 11}, false, XminCommitted | XmaxInvalid},
		{"delete still running", Version{Xmin: 10, Xmax: 12}, false, XminCommitted},
		{"deleted, known by hint, clog says running", Version{Xmin: 10, Xmax: 12, Hints: XminCommitted | XmaxCommitted}, true, 0},
		{"frozen row deleted long ago", Version{Xmin: 11, Xmax: 10, Hints: XminFrozen}, true, 0},
		{"frozen live row keeps its frozen bits", Version{Xmin: 11, Hints: XminFrozen}, false, XminFrozen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			v.Ctid = tid(0, 1)
			h := &Heap{Versions: []*Version{&v}}
			n := Vacuum(h, clog, horizon)
			if removed := h.Versions[0] == nil; removed != tc.wantRemoved || (n == 1) != tc.wantRemoved {
				t.Fatalf("removed=%v count=%d, want removed=%v", removed, n, tc.wantRemoved)
			}
			if !tc.wantRemoved && v.Hints != tc.wantHints {
				t.Fatalf("hints = %04b, want %04b", v.Hints, tc.wantHints)
			}
		})
	}
}

func TestP9_20_Vacuum_WraparoundHorizon(t *testing.T) {
	// The counter wrapped; horizon 5 is "after" 4294967290.
	clog := clogWith(t, map[types.TxnID]TxnStatus{4294967280: StatusCommitted, 4294967290: StatusCommitted, 7: StatusCommitted})
	h := &Heap{Versions: []*Version{
		{Xmin: 4294967280, Xmax: 4294967290, Ctid: tid(0, 1)}, // deleted before the horizon
		{Xmin: 4294967280, Xmax: 7, Ctid: tid(0, 2)},          // deleted after the horizon
	}}
	if got := Vacuum(h, clog, 5); got != 1 || h.Versions[0] != nil || h.Versions[1] == nil {
		t.Fatalf("Vacuum = %d, versions %v, want 1 with only (0,1) removed", got, h.Versions)
	}
}

func TestP9_20_Vacuum_RespectsOldSnapshots(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1, 100)) // xid 3
	reader := db.Begin(RepeatableRead)
	testkit.AssertRows(t, reader.Select(), []types.Row{testkit.R(1, 100)})
	writer := db.Begin(ReadCommitted)
	writer.Update(all, setV(func(v int64) int64 { return v * 2 }))
	writer.Commit()

	horizon := db.Mgr.GetSnapshot(types.InvalidTxnID).Xmin // the reader (4) is still running
	if horizon != reader.Xid {
		t.Fatalf("horizon = %d, want %d", horizon, reader.Xid)
	}
	if got := Vacuum(db.Heap, db.Mgr.Clog, horizon); got != 0 {
		t.Fatalf("Vacuum with an old snapshot alive = %d, want 0", got)
	}
	testkit.AssertRows(t, reader.Select(), []types.Row{testkit.R(1, 100)})

	reader.Commit()
	horizon = db.Mgr.GetSnapshot(types.InvalidTxnID).Xmin
	if got := Vacuum(db.Heap, db.Mgr.Clog, horizon); got != 1 {
		t.Fatalf("Vacuum after reader ended = %d, want 1", got)
	}
	if db.Heap.Versions[0] != nil {
		t.Fatal("old version (0,1) not removed")
	}
	testkit.AssertRows(t, db.Begin(ReadCommitted).Select(), []types.Row{testkit.R(1, 200)})
}

func TestP9_20_Vacuum_AbortedUpdateFixesCtid(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1, "a"))
	tx := db.Begin(ReadCommitted)
	tx.Update(all, func(types.Row) types.Row { return testkit.R(1, "b") })
	tx.Abort()
	if got := Vacuum(db.Heap, db.Mgr.Clog, db.Mgr.NextXid); got != 1 {
		t.Fatalf("Vacuum = %d, want 1 (the aborted new version)", got)
	}
	v := db.Heap.Versions[0]
	if v.Ctid != tid(0, 1) || v.Hints != XminCommitted|XmaxInvalid {
		t.Fatalf("(0,1) = ctid %v hints %04b, want (0,1) XminCommitted|XmaxInvalid", v.Ctid, v.Hints)
	}
	testkit.AssertRows(t, db.Begin(ReadCommitted).Select(), []types.Row{testkit.R(1, "a")})
}

func TestP9_21_Freeze_DocExample(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{100: StatusCommitted, 200: StatusCommitted, 150: StatusAborted})
	h := &Heap{Versions: []*Version{
		{Xmin: 100, Ctid: tid(0, 1)},
		{Xmin: 100, Xmax: 150, Cmax: 3, Ctid: tid(0, 2)},
		{Xmin: 200, Ctid: tid(0, 3)},
		{Xmin: 150, Ctid: tid(0, 4)},
		nil,
	}}
	if got := Freeze(h, clog, 180); got != 2 {
		t.Fatalf("Freeze = %d, want 2", got)
	}
	want := []struct {
		xmin, xmax types.TxnID
		hints      InfoMask
	}{
		{100, 0, XminFrozen},
		{100, 0, XminFrozen | XmaxInvalid},
		{200, 0, 0},
		{150, 0, 0},
	}
	for i, w := range want {
		v := h.Versions[i]
		if v.Xmin != w.xmin || v.Xmax != w.xmax || v.Hints != w.hints {
			t.Errorf("version %d = {Xmin:%d Xmax:%d Hints:%04b}, want {Xmin:%d Xmax:%d Hints:%04b}", i, v.Xmin, v.Xmax, v.Hints, w.xmin, w.xmax, w.hints)
		}
	}
	if h.Versions[1].Cmax != 0 {
		t.Errorf("cleared xmax kept Cmax %d, want 0", h.Versions[1].Cmax)
	}
	if got := Freeze(h, clog, 180); got != 0 {
		t.Fatalf("second Freeze = %d, want 0 (already frozen)", got)
	}
}

func TestP9_21_Freeze_Rules(t *testing.T) {
	clog := clogWith(t, map[types.TxnID]TxnStatus{
		100: StatusCommitted, 101: StatusInProgress, 102: StatusAborted, 103: StatusCommitted, 300: StatusAborted,
	})
	tests := []struct {
		name       string
		v          Version
		wantFrozen bool
		wantXmax   types.TxnID
		wantHints  InfoMask
	}{
		{"running inserter", Version{Xmin: 101}, false, 0, 0},
		{"xmin == cutoff does not precede it", Version{Xmin: 200}, false, 0, 0},
		{"committed by hint even though clog says running", Version{Xmin: 101, Hints: XminCommitted}, true, 0, XminFrozen},
		{"special xmin is never frozen", Version{Xmin: FrozenTxnID}, false, 0, 0},
		{"committed deleter is kept", Version{Xmin: 100, Xmax: 103}, true, 103, XminFrozen},
		{"running deleter is kept", Version{Xmin: 100, Xmax: 101}, true, 101, XminFrozen},
		{"aborted deleter newer than cutoff is kept", Version{Xmin: 100, Xmax: 300}, true, 300, XminFrozen},
		{"aborted deleter cleared even if xmin is not frozen", Version{Xmin: 101, Xmax: 102}, false, 0, XmaxInvalid},
		{"aborted deleter known by hint", Version{Xmin: 100, Xmax: 103, Hints: XmaxInvalid}, true, 0, XminFrozen | XmaxInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			h := &Heap{Versions: []*Version{&v}}
			n := Freeze(h, clog, 200)
			if (n == 1) != tc.wantFrozen || v.Xmax != tc.wantXmax || v.Hints != tc.wantHints {
				t.Fatalf("Freeze → count %d, Xmax %d, Hints %04b; want frozen=%v Xmax %d Hints %04b",
					n, v.Xmax, v.Hints, tc.wantFrozen, tc.wantXmax, tc.wantHints)
			}
		})
	}
}

func TestP9_21_Freeze_SavesRowsFromWraparound(t *testing.T) {
	// T100 inserted two rows long ago and committed. 1 000 000 xids later
	// VACUUM FREEZE runs on one copy only. Then the counter advances to just past
	// 2^31 xids after 100.
	m := NewTxnManager()
	if err := m.Clog.Set(100, StatusCommitted); err != nil {
		t.Fatal(err)
	}
	frozen := &Heap{}
	frozen.Insert(100, 0, testkit.R("frozen"))
	plain := &Heap{}
	plain.Insert(100, 0, testkit.R("plain"))
	if got := Freeze(frozen, m.Clog, 1_000_100); got != 1 {
		t.Fatalf("Freeze = %d, want 1", got)
	}

	visible := func(h *Heap, nextXid types.TxnID) bool {
		m.NextXid = nextXid
		return TupleVisible(h.Versions[0], m.GetSnapshot(types.InvalidTxnID), m.Clog, nil)
	}
	// 2^31 xids after 100: still (barely) in the past.
	if !visible(plain, 2147483748) || !visible(frozen, 2147483748) {
		t.Fatal("rows invisible while xid 100 still precedes the snapshot")
	}
	// One more xid: 100 now looks like it is in the future.
	if visible(plain, 2147483749) {
		t.Fatal("unfrozen row from xid 100 still visible after wraparound — expected it to vanish")
	}
	if !visible(frozen, 2147483749) {
		t.Fatal("frozen row vanished after wraparound")
	}
}
