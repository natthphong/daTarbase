package txn

import (
	"errors"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP9_4_Begin(t *testing.T) {
	m := NewTxnManager()
	for i, want := range []types.TxnID{3, 4, 5} {
		if got := m.Begin(); got != want {
			t.Fatalf("Begin #%d = %d, want %d", i, got, want)
		}
	}
	if m.NextXid != 6 {
		t.Fatalf("NextXid = %d, want 6", m.NextXid)
	}
	for _, x := range []types.TxnID{3, 4, 5} {
		if !m.Running[x] {
			t.Fatalf("xid %d not in Running", x)
		}
		if s := m.Clog.Get(x); s != StatusInProgress {
			t.Fatalf("clog[%d] = %v, want in-progress", x, s)
		}
	}
	if len(m.Running) != 3 {
		t.Fatalf("len(Running) = %d, want 3", len(m.Running))
	}
}

func TestP9_4_Begin_Wraparound(t *testing.T) {
	m := NewTxnManager()
	m.NextXid = 4294967294
	// The clog still remembers xid 3 from the previous epoch as aborted.
	if err := m.Clog.Set(3, StatusAborted); err != nil {
		t.Fatal(err)
	}
	want := []types.TxnID{4294967294, 4294967295, 3, 4}
	for i, w := range want {
		if got := m.Begin(); got != w {
			t.Fatalf("Begin #%d = %d, want %d (0, 1, 2 must be skipped)", i, got, w)
		}
	}
	if m.NextXid != 5 {
		t.Fatalf("NextXid = %d, want 5", m.NextXid)
	}
	if s := m.Clog.Get(3); s != StatusInProgress {
		t.Fatalf("clog[3] = %v after reuse, want in-progress (stale status overwritten)", s)
	}
}

func TestP9_4_Begin_LastXidWrapsToFirstNormal(t *testing.T) {
	m := NewTxnManager()
	m.NextXid = 4294967295
	if got := m.Begin(); got != 4294967295 {
		t.Fatalf("Begin = %d, want 4294967295", got)
	}
	if m.NextXid != FirstNormalTxnID {
		t.Fatalf("NextXid = %d, want %d", m.NextXid, FirstNormalTxnID)
	}
}

func TestP9_5_End(t *testing.T) {
	m := NewTxnManager()
	a, b, c := m.Begin(), m.Begin(), m.Begin() // 3, 4, 5
	if err := m.End(a, true); err != nil {
		t.Fatalf("commit 3: %v", err)
	}
	if err := m.End(b, false); err != nil {
		t.Fatalf("abort 4: %v", err)
	}
	if s := m.Clog.Get(a); s != StatusCommitted {
		t.Fatalf("clog[3] = %v, want committed", s)
	}
	if s := m.Clog.Get(b); s != StatusAborted {
		t.Fatalf("clog[4] = %v, want aborted", s)
	}
	if s := m.Clog.Get(c); s != StatusInProgress {
		t.Fatalf("clog[5] = %v, want in-progress", s)
	}
	if len(m.Running) != 1 || !m.Running[c] {
		t.Fatalf("Running = %v, want {5}", m.Running)
	}
}

func TestP9_5_End_Errors(t *testing.T) {
	tests := []struct {
		name   string
		xid    types.TxnID
		commit bool
	}{
		{"commit twice", 3, true},
		{"abort after commit", 3, false},
		{"commit after abort", 4, true},
		{"never begun", 99, true},
		{"invalid xid", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewTxnManager()
			m.Begin() // 3
			m.Begin() // 4
			m.Begin() // 5
			if err := m.End(3, true); err != nil {
				t.Fatal(err)
			}
			if err := m.End(4, false); err != nil {
				t.Fatal(err)
			}
			if err := m.End(tc.xid, tc.commit); !errors.Is(err, ErrNotInProgress) {
				t.Fatalf("End(%d, %v) = %v, want ErrNotInProgress", tc.xid, tc.commit, err)
			}
			if m.Clog.Get(3) != StatusCommitted || m.Clog.Get(4) != StatusAborted {
				t.Fatalf("failed End changed the clog: 3=%v 4=%v", m.Clog.Get(3), m.Clog.Get(4))
			}
			if len(m.Running) != 1 {
				t.Fatalf("Running = %v, want {5}", m.Running)
			}
		})
	}
}

// buildSubxacts: top 3 with child 4 (whose child is 6), unrelated top 5,
// and an already-aborted child 7 of 3.
func buildSubxacts(t *testing.T) *TxnManager {
	t.Helper()
	m := NewTxnManager()
	top, sub, other, grand, dead := m.Begin(), m.Begin(), m.Begin(), m.Begin(), m.Begin() // 3 4 5 6 7
	m.Parent[sub] = top
	m.Parent[grand] = sub
	m.Parent[dead] = top
	if err := m.Clog.Set(dead, StatusAborted); err != nil {
		t.Fatal(err)
	}
	delete(m.Running, dead)
	_ = other
	return m
}

func TestP9_5_End_Subtransactions(t *testing.T) {
	for _, commit := range []bool{true, false} {
		m := buildSubxacts(t)
		if err := m.End(3, commit); err != nil {
			t.Fatalf("End(3, %v) = %v", commit, err)
		}
		want := StatusAborted
		if commit {
			want = StatusCommitted
		}
		for _, x := range []types.TxnID{3, 4, 6} {
			if s := m.Clog.Get(x); s != want {
				t.Fatalf("commit=%v: clog[%d] = %v, want %v", commit, x, s, want)
			}
		}
		if s := m.Clog.Get(7); s != StatusAborted {
			t.Fatalf("commit=%v: rolled-back child 7 = %v, want to stay aborted", commit, s)
		}
		if s := m.Clog.Get(5); s != StatusInProgress {
			t.Fatalf("commit=%v: unrelated 5 = %v, want in-progress", commit, s)
		}
		if len(m.Running) != 1 || !m.Running[5] {
			t.Fatalf("commit=%v: Running = %v, want {5}", commit, m.Running)
		}
	}
}

func TestP9_5_End_SubxactDirectly(t *testing.T) {
	m := buildSubxacts(t)
	if err := m.End(4, true); !errors.Is(err, ErrSubxact) {
		t.Fatalf("End(subxact 4) = %v, want ErrSubxact", err)
	}
	if m.Clog.Get(4) != StatusInProgress || !m.Running[4] {
		t.Fatalf("failed End changed subxact 4")
	}
}

func assertSnap(t *testing.T, got *Snapshot, xmin, xmax types.TxnID, xip []types.TxnID) {
	t.Helper()
	if got.Xmin != xmin || got.Xmax != xmax || !slices.Equal(got.Xip, xip) || got.CurCid != 0 {
		t.Fatalf("snapshot = {Xmin:%d Xmax:%d Xip:%v CurCid:%d}, want {Xmin:%d Xmax:%d Xip:%v CurCid:0}",
			got.Xmin, got.Xmax, got.Xip, got.CurCid, xmin, xmax, xip)
	}
}

func TestP9_6_GetSnapshot(t *testing.T) {
	m := NewTxnManager()
	assertSnap(t, m.GetSnapshot(types.InvalidTxnID), 3, 3, nil) // nothing ever ran

	t3, t4, t5 := m.Begin(), m.Begin(), m.Begin()
	if err := m.Commit(t4); err != nil {
		t.Fatal(err)
	}
	assertSnap(t, m.GetSnapshot(t5), 3, 6, []types.TxnID{3})
	assertSnap(t, m.GetSnapshot(t3), 3, 6, []types.TxnID{5}) // own xid still counts for Xmin
	assertSnap(t, m.GetSnapshot(types.InvalidTxnID), 3, 6, []types.TxnID{3, 5})

	if err := m.Commit(t3); err != nil {
		t.Fatal(err)
	}
	assertSnap(t, m.GetSnapshot(t5), 5, 6, nil)
	if err := m.Abort(t5); err != nil {
		t.Fatal(err)
	}
	assertSnap(t, m.GetSnapshot(types.InvalidTxnID), 6, 6, nil) // nothing running → Xmin = Xmax
}

func TestP9_6_GetSnapshot_ExcludesOwnSubxacts(t *testing.T) {
	m := buildSubxacts(t) // running: 3 (top), 4 (sub of 3), 5 (top), 6 (sub of 4)
	assertSnap(t, m.GetSnapshot(3), 3, 8, []types.TxnID{5})
	assertSnap(t, m.GetSnapshot(5), 3, 8, []types.TxnID{3, 4, 6}) // others' subxacts are running xids
}

func TestP9_6_GetSnapshot_AcrossWraparound(t *testing.T) {
	m := NewTxnManager()
	m.NextXid = 4294967294
	a, b, c, d := m.Begin(), m.Begin(), m.Begin(), m.Begin() // 4294967294, 4294967295, 3, 4
	if err := m.Commit(b); err != nil {
		t.Fatal(err)
	}
	// Circular order: 4294967294 < 3 < 4 < 5.
	assertSnap(t, m.GetSnapshot(d), a, 5, []types.TxnID{a, c})
	assertSnap(t, m.GetSnapshot(types.InvalidTxnID), a, 5, []types.TxnID{a, c, d})
}

func TestP9_7_XidInSnapshot(t *testing.T) {
	snap := &Snapshot{Xmin: 100, Xmax: 105, Xip: []types.TxnID{100, 102}}
	tests := []struct {
		xid  types.TxnID
		want bool
		why  string
	}{
		{99, false, "precedes Xmin: finished"},
		{100, true, "in Xip"},
		{101, false, "between, not in Xip: finished"},
		{102, true, "in Xip"},
		{103, false, "not in Xip"},
		{104, false, "last xid before Xmax, not in Xip"},
		{105, true, "== Xmax: started after the snapshot"},
		{200, true, "> Xmax"},
		{FrozenTxnID, false, "special xid"},
		{types.InvalidTxnID, false, "invalid xid"},
	}
	for _, tc := range tests {
		if got := XidInSnapshot(tc.xid, snap); got != tc.want {
			t.Errorf("XidInSnapshot(%d) = %v, want %v (%s)", tc.xid, got, tc.want, tc.why)
		}
	}
}

func TestP9_7_XidInSnapshot_EmptyXip(t *testing.T) {
	snap := &Snapshot{Xmin: 50, Xmax: 50}
	for xid, want := range map[types.TxnID]bool{49: false, 50: true, 51: true, 3: false} {
		if got := XidInSnapshot(xid, snap); got != want {
			t.Errorf("XidInSnapshot(%d) with Xmin=Xmax=50 = %v, want %v", xid, got, want)
		}
	}
}

func TestP9_7_XidInSnapshot_Wraparound(t *testing.T) {
	// Taken just after the counter wrapped: Xip spans the wrap point.
	snap := &Snapshot{Xmin: 4294967290, Xmax: 10, Xip: []types.TxnID{4294967290, 4294967295, 4, 8}}
	tests := []struct {
		xid  types.TxnID
		want bool
	}{
		{4294967289, false}, // precedes Xmin
		{4294967290, true},
		{4294967291, false},
		{4294967295, true},
		{3, false},
		{4, true},
		{8, true},
		{9, false},
		{10, true},   // == Xmax
		{1000, true}, // future
	}
	for _, tc := range tests {
		if got := XidInSnapshot(tc.xid, snap); got != tc.want {
			t.Errorf("XidInSnapshot(%d) = %v, want %v", tc.xid, got, tc.want)
		}
	}
}

func TestP9_7_XidInSnapshot_LargeXipBinarySearch(t *testing.T) {
	// Every even xid in [1000, 3000) is running.
	snap := &Snapshot{Xmin: 1000, Xmax: 3000}
	for x := types.TxnID(1000); x < 3000; x += 2 {
		snap.Xip = append(snap.Xip, x)
	}
	for x := types.TxnID(1000); x < 3000; x++ {
		if got, want := XidInSnapshot(x, snap), x%2 == 0; got != want {
			t.Fatalf("XidInSnapshot(%d) = %v, want %v", x, got, want)
		}
	}
}
