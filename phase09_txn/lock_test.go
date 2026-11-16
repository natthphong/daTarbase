package txn

import (
	"errors"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP9_16_LockConflicts_Matrix(t *testing.T) {
	// Row = requested mode, column = held mode, 1 = conflict.
	want := [8]string{
		"00000001", // AccessShare
		"00000011", // RowShare
		"00001111", // RowExclusive
		"00011111", // ShareUpdateExclusive
		"00110111", // Share
		"00111111", // ShareRowExclusive
		"01111111", // Exclusive
		"11111111", // AccessExclusive
	}
	for a := LockMode(1); a <= 8; a++ {
		for b := LockMode(1); b <= 8; b++ {
			w := want[a-1][b-1] == '1'
			if got := LockConflicts(a, b); got != w {
				t.Errorf("LockConflicts(%d, %d) = %v, want %v", a, b, got, w)
			}
			if LockConflicts(a, b) != LockConflicts(b, a) {
				t.Errorf("matrix not symmetric at (%d, %d)", a, b)
			}
		}
	}
}

func TestP9_16_LockConflicts_Examples(t *testing.T) {
	tests := []struct {
		name string
		a, b LockMode
		want bool
	}{
		{"SELECT vs DROP TABLE", AccessShareLock, AccessExclusiveLock, true},
		{"two writers at table level", RowExclusiveLock, RowExclusiveLock, false},
		{"CREATE INDEX vs INSERT", ShareLock, RowExclusiveLock, true},
		{"two CREATE INDEX", ShareLock, ShareLock, false},
		{"two VACUUMs (SUX self-conflicts)", ShareUpdateExclusiveLock, ShareUpdateExclusiveLock, true},
		{"SELECT vs Exclusive (REFRESH MATVIEW CONCURRENTLY)", AccessShareLock, ExclusiveLock, false},
		{"row FOR SHARE vs FOR SHARE", ShareLock, ShareLock, false},
		{"row FOR SHARE vs FOR UPDATE", ShareLock, ExclusiveLock, true},
	}
	for _, tc := range tests {
		if got := LockConflicts(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: LockConflicts(%d, %d) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

func reqs(pairs ...any) []LockRequest {
	var out []LockRequest
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, LockRequest{Xid: types.TxnID(pairs[i].(int)), Mode: pairs[i+1].(LockMode)})
	}
	return out
}

func assertQueue(t *testing.T, lm *LockManager, tag LockTag, granted, waiting []LockRequest) {
	t.Helper()
	if g := lm.Holders(tag); !slices.Equal(g, granted) {
		t.Fatalf("%s granted = %v, want %v", tag, g, granted)
	}
	if w := lm.Waiters(tag); !slices.Equal(w, waiting) {
		t.Fatalf("%s waiting = %v, want %v", tag, w, waiting)
	}
}

type acq struct {
	xid  types.TxnID
	tag  LockTag
	mode LockMode
}

func mustAcquire(t *testing.T, lm *LockManager, steps ...acq) {
	t.Helper()
	for _, s := range steps {
		if _, err := lm.Acquire(s.xid, s.tag, s.mode); err != nil {
			t.Fatalf("Acquire(%d, %s, %d): %v", s.xid, s.tag, s.mode, err)
		}
	}
}

func TestP9_17_Acquire_DocExample(t *testing.T) {
	lm := NewLockManager()
	steps := []struct {
		xid     types.TxnID
		tag     LockTag
		mode    LockMode
		want    bool
		wantErr error
	}{
		{10, "rel:t", AccessShareLock, true, nil},
		{11, "rel:t", AccessExclusiveLock, false, nil},
		{12, "rel:t", AccessShareLock, false, nil},
		{11, "rel:u", AccessShareLock, false, ErrAlreadyWaiting},
		{10, "rel:t", AccessShareLock, true, nil},
	}
	for i, s := range steps {
		got, err := lm.Acquire(s.xid, s.tag, s.mode)
		if got != s.want || !errors.Is(err, s.wantErr) {
			t.Fatalf("step %d Acquire(%d, %s, %d) = (%v, %v), want (%v, %v)", i, s.xid, s.tag, s.mode, got, err, s.want, s.wantErr)
		}
	}
	assertQueue(t, lm, "rel:t", reqs(10, AccessShareLock), reqs(11, AccessExclusiveLock, 12, AccessShareLock))
	if _, ok := lm.Locks["rel:u"]; ok {
		t.Fatal("rejected request created an entry for rel:u")
	}
}

func TestP9_17_Acquire(t *testing.T) {
	tests := []struct {
		name        string
		steps       []acq
		last        acq
		want        bool
		wantGranted []LockRequest
		wantWaiting []LockRequest
	}{
		{
			name:        "readers and writers share the table",
			steps:       []acq{{10, "rel:t", AccessShareLock}, {11, "rel:t", RowExclusiveLock}},
			last:        acq{12, "rel:t", RowExclusiveLock},
			want:        true,
			wantGranted: reqs(10, AccessShareLock, 11, RowExclusiveLock, 12, RowExclusiveLock),
		},
		{
			name:        "own locks never block (upgrade when alone)",
			steps:       []acq{{10, "rel:t", AccessShareLock}},
			last:        acq{10, "rel:t", AccessExclusiveLock},
			want:        true,
			wantGranted: reqs(10, AccessShareLock, 10, AccessExclusiveLock),
		},
		{
			name:        "upgrade blocked by another holder",
			steps:       []acq{{10, "rel:t", AccessShareLock}, {11, "rel:t", AccessShareLock}},
			last:        acq{10, "rel:t", AccessExclusiveLock},
			want:        false,
			wantGranted: reqs(10, AccessShareLock, 11, AccessShareLock),
			wantWaiting: reqs(10, AccessExclusiveLock),
		},
		{
			name:        "row FOR SHARE twice is fine",
			steps:       []acq{{10, "row:t(0,1)", ShareLock}},
			last:        acq{11, "row:t(0,1)", ShareLock},
			want:        true,
			wantGranted: reqs(10, ShareLock, 11, ShareLock),
		},
		{
			name:        "FOR SHARE queues behind a waiting FOR UPDATE",
			steps:       []acq{{10, "row:t(0,1)", ShareLock}, {11, "row:t(0,1)", ExclusiveLock}},
			last:        acq{12, "row:t(0,1)", ShareLock},
			want:        false,
			wantGranted: reqs(10, ShareLock),
			wantWaiting: reqs(11, ExclusiveLock, 12, ShareLock),
		},
		{
			name:        "non-conflicting request passes the queue",
			steps:       []acq{{10, "rel:t", RowExclusiveLock}, {11, "rel:t", ShareLock}},
			last:        acq{12, "rel:t", AccessShareLock},
			want:        true,
			wantGranted: reqs(10, RowExclusiveLock, 12, AccessShareLock),
			wantWaiting: reqs(11, ShareLock),
		},
		{
			name:        "re-acquire a held mode is a no-op",
			steps:       []acq{{10, "rel:t", ExclusiveLock}},
			last:        acq{10, "rel:t", ExclusiveLock},
			want:        true,
			wantGranted: reqs(10, ExclusiveLock),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lm := NewLockManager()
			mustAcquire(t, lm, tc.steps...)
			got, err := lm.Acquire(tc.last.xid, tc.last.tag, tc.last.mode)
			if got != tc.want || err != nil {
				t.Fatalf("Acquire = (%v, %v), want (%v, nil)", got, err, tc.want)
			}
			assertQueue(t, lm, tc.last.tag, tc.wantGranted, tc.wantWaiting)
		})
	}
}

func TestP9_17_Acquire_Errors(t *testing.T) {
	lm := NewLockManager()
	for _, mode := range []LockMode{0, 9} {
		if ok, err := lm.Acquire(10, "rel:t", mode); ok || !errors.Is(err, ErrBadLockMode) {
			t.Fatalf("Acquire(mode %d) = (%v, %v), want (false, ErrBadLockMode)", mode, ok, err)
		}
	}
	if len(lm.Locks) != 0 {
		t.Fatalf("bad requests created %d lock entries", len(lm.Locks))
	}
	mustAcquire(t, lm, acq{10, "rel:t", ExclusiveLock}, acq{11, "rel:t", ExclusiveLock})
	// 11 is asleep: even a request it already holds or one on another tag is refused.
	for _, tag := range []LockTag{"rel:t", "rel:other"} {
		if ok, err := lm.Acquire(11, tag, AccessShareLock); ok || !errors.Is(err, ErrAlreadyWaiting) {
			t.Fatalf("Acquire by waiting xid on %s = (%v, %v), want (false, ErrAlreadyWaiting)", tag, ok, err)
		}
	}
	assertQueue(t, lm, "rel:t", reqs(10, ExclusiveLock), reqs(11, ExclusiveLock))
}

func TestP9_18_Release_DocExample(t *testing.T) {
	lm := NewLockManager()
	mustAcquire(t, lm,
		acq{10, "rel:t", AccessShareLock}, acq{11, "rel:t", AccessExclusiveLock},
		acq{12, "rel:t", AccessShareLock}, acq{13, "rel:t", AccessShareLock})
	assertQueue(t, lm, "rel:t", reqs(10, AccessShareLock), reqs(11, AccessExclusiveLock, 12, AccessShareLock, 13, AccessShareLock))

	if got := lm.Release(10, "rel:t"); !slices.Equal(got, reqs(11, AccessExclusiveLock)) {
		t.Fatalf("Release(10) woke %v, want [{11 AX}]", got)
	}
	assertQueue(t, lm, "rel:t", reqs(11, AccessExclusiveLock), reqs(12, AccessShareLock, 13, AccessShareLock))
	if got := lm.Release(11, "rel:t"); !slices.Equal(got, reqs(12, AccessShareLock, 13, AccessShareLock)) {
		t.Fatalf("Release(11) woke %v, want [{12 AS} {13 AS}]", got)
	}
	assertQueue(t, lm, "rel:t", reqs(12, AccessShareLock, 13, AccessShareLock), nil)
	lm.Release(12, "rel:t")
	if got := lm.Release(13, "rel:t"); got != nil {
		t.Fatalf("Release(13) woke %v, want nil", got)
	}
	if len(lm.Locks) != 0 {
		t.Fatalf("empty tag not deleted: %v", lm.Locks)
	}
}

func TestP9_18_Release_StrictFIFO(t *testing.T) {
	lm := NewLockManager()
	mustAcquire(t, lm,
		acq{10, "row:1", ExclusiveLock}, acq{11, "row:1", ShareLock},
		acq{12, "row:1", ExclusiveLock}, acq{13, "row:1", ShareLock})
	// 13's Share is compatible with 11's Share, but 12's Exclusive is ahead of it.
	if got := lm.Release(10, "row:1"); !slices.Equal(got, reqs(11, ShareLock)) {
		t.Fatalf("Release(10) woke %v, want [{11 S}]", got)
	}
	assertQueue(t, lm, "row:1", reqs(11, ShareLock), reqs(12, ExclusiveLock, 13, ShareLock))
	if got := lm.Release(11, "row:1"); !slices.Equal(got, reqs(12, ExclusiveLock)) {
		t.Fatalf("Release(11) woke %v, want [{12 X}]", got)
	}
	if got := lm.Release(12, "row:1"); !slices.Equal(got, reqs(13, ShareLock)) {
		t.Fatalf("Release(12) woke %v, want [{13 S}]", got)
	}
}

func TestP9_18_Release_CancelWaiter(t *testing.T) {
	lm := NewLockManager()
	mustAcquire(t, lm, acq{10, "rel:t", AccessShareLock}, acq{11, "rel:t", AccessExclusiveLock}, acq{12, "rel:t", AccessShareLock})
	// 11 gives up waiting (e.g. lock_timeout): 12 is no longer stuck behind it.
	if got := lm.Release(11, "rel:t"); !slices.Equal(got, reqs(12, AccessShareLock)) {
		t.Fatalf("Release(waiting 11) woke %v, want [{12 AS}]", got)
	}
	assertQueue(t, lm, "rel:t", reqs(10, AccessShareLock, 12, AccessShareLock), nil)
	// 11 may now request again.
	if ok, err := lm.Acquire(11, "rel:t", AccessShareLock); !ok || err != nil {
		t.Fatalf("Acquire after cancel = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestP9_18_Release_AllModesAndUpgrade(t *testing.T) {
	lm := NewLockManager()
	mustAcquire(t, lm, acq{10, "rel:t", AccessShareLock}, acq{10, "rel:t", RowExclusiveLock}, acq{11, "rel:t", ShareLock})
	if got := lm.Release(10, "rel:t"); !slices.Equal(got, reqs(11, ShareLock)) {
		t.Fatalf("Release(10 holding AS+RX) woke %v, want [{11 S}]", got)
	}
	assertQueue(t, lm, "rel:t", reqs(11, ShareLock), nil)

	lm = NewLockManager()
	mustAcquire(t, lm, acq{10, "rel:t", AccessShareLock}, acq{11, "rel:t", AccessShareLock}, acq{10, "rel:t", AccessExclusiveLock})
	if got := lm.Release(11, "rel:t"); !slices.Equal(got, reqs(10, AccessExclusiveLock)) {
		t.Fatalf("Release(11) woke %v, want [{10 AX}] (own AS does not block the upgrade)", got)
	}
	assertQueue(t, lm, "rel:t", reqs(10, AccessShareLock, 10, AccessExclusiveLock), nil)
}

func TestP9_18_Release_NothingHeld(t *testing.T) {
	lm := NewLockManager()
	if got := lm.Release(10, "rel:none"); got != nil {
		t.Fatalf("Release on unknown tag = %v, want nil", got)
	}
	mustAcquire(t, lm, acq{10, "rel:t", ExclusiveLock}, acq{11, "rel:t", ExclusiveLock})
	if got := lm.Release(99, "rel:t"); got != nil {
		t.Fatalf("Release by non-holder = %v, want nil", got)
	}
	assertQueue(t, lm, "rel:t", reqs(10, ExclusiveLock), reqs(11, ExclusiveLock))
}

func TestP9_19_DetectDeadlock(t *testing.T) {
	tests := []struct {
		name       string
		steps      []acq
		wantDead   []types.TxnID
		wantVictim types.TxnID
	}{
		{
			name:  "no waiting at all",
			steps: []acq{{10, "rel:t", AccessShareLock}, {11, "rel:t", RowExclusiveLock}},
		},
		{
			name:  "plain wait is not a deadlock",
			steps: []acq{{10, "row:1", ExclusiveLock}, {11, "row:1", ExclusiveLock}, {12, "row:1", ShareLock}},
		},
		{
			name: "classic two-row deadlock",
			steps: []acq{
				{10, "row:1", ExclusiveLock}, {11, "row:2", ExclusiveLock},
				{10, "row:2", ExclusiveLock}, {11, "row:1", ExclusiveLock},
			},
			wantDead: []types.TxnID{10, 11}, wantVictim: 11,
		},
		{
			name: "lock upgrade deadlock",
			steps: []acq{
				{10, "rel:t", AccessShareLock}, {11, "rel:t", AccessShareLock},
				{10, "rel:t", AccessExclusiveLock}, {11, "rel:t", AccessExclusiveLock},
			},
			wantDead: []types.TxnID{10, 11}, wantVictim: 11,
		},
		{
			name: "three-way cycle with a bystander waiting on it",
			steps: []acq{
				{20, "A", ExclusiveLock}, {21, "B", ExclusiveLock}, {22, "C", ExclusiveLock},
				{20, "B", ExclusiveLock}, {21, "C", ExclusiveLock}, {22, "A", ExclusiveLock},
				{23, "A", ExclusiveLock},
			},
			wantDead: []types.TxnID{20, 21, 22}, wantVictim: 22,
		},
		{
			name: "two independent cycles: youngest overall is the victim",
			steps: []acq{
				{30, "P", ExclusiveLock}, {35, "Q", ExclusiveLock}, {30, "Q", ExclusiveLock}, {35, "P", ExclusiveLock},
				{32, "R", ExclusiveLock}, {33, "S", ExclusiveLock}, {32, "S", ExclusiveLock}, {33, "R", ExclusiveLock},
			},
			wantDead: []types.TxnID{30, 32, 33, 35}, wantVictim: 35,
		},
		{
			name: "cycle that needs a soft (queue-order) edge",
			steps: []acq{
				{10, "B", RowShareLock},  // granted
				{11, "B", ExclusiveLock}, // waits for 10 (X conflicts RS)
				{10, "B", ShareLock},     // S is compatible with RS, but waits behind 11's X
			},
			wantDead: []types.TxnID{10, 11}, wantVictim: 11,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lm := NewLockManager()
			mustAcquire(t, lm, tc.steps...)
			dead, victim := lm.DetectDeadlock()
			if !slices.Equal(dead, tc.wantDead) || victim != tc.wantVictim {
				t.Fatalf("DetectDeadlock = (%v, %d), want (%v, %d)", dead, victim, tc.wantDead, tc.wantVictim)
			}
		})
	}
}

func TestP9_19_DetectDeadlock_ResolvedAfterVictimReleases(t *testing.T) {
	lm := NewLockManager()
	mustAcquire(t, lm,
		acq{10, "row:1", ExclusiveLock}, acq{11, "row:2", ExclusiveLock},
		acq{10, "row:2", ExclusiveLock}, acq{11, "row:1", ExclusiveLock})
	_, victim := lm.DetectDeadlock()
	if victim != 11 {
		t.Fatalf("victim = %d, want 11", victim)
	}
	// Abort the victim: release everything it has.
	lm.Release(victim, "row:1")
	woken := lm.Release(victim, "row:2")
	if !slices.Equal(woken, reqs(10, ExclusiveLock)) {
		t.Fatalf("releasing victim woke %v, want [{10 X}]", woken)
	}
	if dead, v := lm.DetectDeadlock(); dead != nil || v != types.InvalidTxnID {
		t.Fatalf("after abort DetectDeadlock = (%v, %d), want (nil, 0)", dead, v)
	}
}
