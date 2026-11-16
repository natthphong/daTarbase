package storage

import (
	"slices"
	"testing"
)

// op is one step of a replacer script.
type op struct {
	kind  byte // 'a' RecordAccess, 'e' SetEvictable(true), 'p' SetEvictable(false), 'r' Remove
	frame FrameID
}

func access(fs ...FrameID) []op {
	var out []op
	for _, f := range fs {
		out = append(out, op{'a', f})
	}
	return out
}

func evictable(fs ...FrameID) []op {
	var out []op
	for _, f := range fs {
		out = append(out, op{'e', f})
	}
	return out
}

func script(parts ...[]op) []op { return slices.Concat(parts...) }

func run(r Replacer, ops []op) {
	for _, o := range ops {
		switch o.kind {
		case 'a':
			r.RecordAccess(o.frame)
		case 'e':
			r.SetEvictable(o.frame, true)
		case 'p':
			r.SetEvictable(o.frame, false)
		case 'r':
			r.Remove(o.frame)
		}
	}
}

// drain evicts until Evict reports false.
func drain(r Replacer) []FrameID {
	var out []FrameID
	for i := 0; i < 100; i++ {
		f, ok := r.Evict()
		if !ok {
			return out
		}
		out = append(out, f)
	}
	return out
}

type evictCase struct {
	name     string
	ops      []op
	wantSize int       // Size() before evicting
	want     []FrameID // victims in order, until Evict returns false
}

func runEvictCases(t *testing.T, newR func() Replacer, cases []evictCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newR()
			run(r, tc.ops)
			if r.Size() != tc.wantSize {
				t.Fatalf("Size() = %d, want %d", r.Size(), tc.wantSize)
			}
			got := drain(r)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("eviction order = %v, want %v", got, tc.want)
			}
			if r.Size() != 0 {
				t.Fatalf("Size() after draining = %d, want 0", r.Size())
			}
		})
	}
}

// ---------------------------------------------------------------- LRU

func TestP3_7_RecordAccess(t *testing.T) {
	tests := []struct {
		name      string
		ops       []op
		wantOrder []FrameID
		wantSize  int
	}{
		{"empty", nil, nil, 0},
		{"new frames go to the back", access(1, 2, 3), []FrameID{1, 2, 3}, 0},
		{"re-access moves to the back", access(1, 2, 3, 1), []FrameID{2, 3, 1}, 0},
		{"re-access of the back is a no-op", access(1, 2, 3, 3), []FrameID{1, 2, 3}, 0},
		{"re-access keeps evictable flag", script(access(1, 2), evictable(1), access(1)), []FrameID{2, 1}, 1},
		{"new frames are not evictable", script(access(4), evictable(4), access(5)), []FrameID{4, 5}, 1},
		{"after Remove a frame is new again", script(access(1, 2), evictable(1), []op{{'r', 1}}, access(1)), []FrameID{2, 1}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewLRUReplacer(8)
			run(r, tc.ops)
			if got := r.lruOrder(); !slices.Equal(got, tc.wantOrder) {
				t.Fatalf("LRU order = %v, want %v", got, tc.wantOrder)
			}
			if r.Size() != tc.wantSize {
				t.Fatalf("Size() = %d, want %d", r.Size(), tc.wantSize)
			}
			if len(r.elems) != len(tc.wantOrder) || len(r.evictable) != len(tc.wantOrder) {
				t.Fatalf("len(elems)=%d len(evictable)=%d, want %d", len(r.elems), len(r.evictable), len(tc.wantOrder))
			}
		})
	}
}

// 100k accesses on 1000 frames: must be O(1) each (an O(n) scan would take ~10^8 steps).
func TestP3_7_RecordAccess_ManyFrames(t *testing.T) {
	r := NewLRUReplacer(1000)
	for i := 0; i < 100_000; i++ {
		r.RecordAccess(FrameID((i * 7) % 1000))
	}
	order := r.lruOrder()
	if len(order) != 1000 {
		t.Fatalf("tracked %d frames, want 1000", len(order))
	}
	// The last 1000 accesses were frames (i*7)%1000 for i = 99000..99999: all distinct.
	for k, f := range order {
		if want := FrameID(((99_000 + k) * 7) % 1000); f != want {
			t.Fatalf("order[%d] = %d, want %d", k, f, want)
		}
	}
}

func TestP3_8_Evict(t *testing.T) {
	runEvictCases(t, func() Replacer { return NewLRUReplacer(8) }, []evictCase{
		{"empty replacer", nil, 0, nil},
		{"only pinned frames", access(1, 2), 0, nil},
		{"least recently used first", script(access(1, 2, 3, 4), evictable(1, 2, 3, 4), access(1)), 4, []FrameID{2, 3, 4, 1}},
		{"pinned frames are skipped", script(access(1, 2, 3), evictable(2, 3)), 2, []FrameID{2, 3}},
		{"pinning again protects", script(access(1, 2, 3), evictable(1, 2, 3), []op{{'p', 1}}), 2, []FrameID{2, 3}},
		{"removed frame is never evicted", script(access(1, 2, 3), evictable(1, 2, 3), []op{{'r', 2}}), 2, []FrameID{1, 3}},
	})
}

func TestP3_8_Evict_VictimIsUntracked(t *testing.T) {
	r := NewLRUReplacer(8)
	run(r, script(access(1, 2, 3), evictable(1, 2)))
	if f, ok := r.Evict(); !ok || f != 1 {
		t.Fatalf("Evict() = %d, %v; want 1, true", f, ok)
	}
	if got := r.lruOrder(); !slices.Equal(got, []FrameID{2, 3}) {
		t.Fatalf("order after evict = %v, want [2 3]", got)
	}
	if r.Size() != 1 {
		t.Fatalf("Size() = %d, want 1", r.Size())
	}
	r.SetEvictable(1, true) // untracked: no-op
	if r.Size() != 1 {
		t.Fatalf("SetEvictable on evicted frame changed Size() to %d, want 1", r.Size())
	}
	r.RecordAccess(1) // tracked again, at the back, pinned
	r.SetEvictable(1, true)
	if got := drain(r); !slices.Equal(got, []FrameID{2, 1}) {
		t.Fatalf("drain = %v, want [2 1] (3 is pinned)", got)
	}
	if got := r.lruOrder(); !slices.Equal(got, []FrameID{3}) {
		t.Fatalf("remaining = %v, want [3]", got)
	}
}

func TestP3_8_Evict_InterleavedWithAccess(t *testing.T) {
	r := NewLRUReplacer(3)
	run(r, script(access(0, 1, 2), evictable(0, 1, 2))) // [0 1 2]
	steps := []struct {
		ops       []op
		want      FrameID
		wantOK    bool
		wantOrder []FrameID
	}{
		{nil, 0, true, []FrameID{1, 2}},
		{access(0, 1), 2, true, []FrameID{0, 1}}, // 0 re-enters pinned; 1 moves to back
		{evictable(0), 0, true, []FrameID{1}},
		{nil, 1, true, nil},
		{nil, 0, false, nil},
	}
	for i, s := range steps {
		run(r, s.ops)
		f, ok := r.Evict()
		if f != s.want || ok != s.wantOK {
			t.Fatalf("step %d: Evict() = %d, %v; want %d, %v", i, f, ok, s.want, s.wantOK)
		}
		if got := r.lruOrder(); !slices.Equal(got, s.wantOrder) {
			t.Fatalf("step %d: order = %v, want %v", i, got, s.wantOrder)
		}
	}
}

// ---------------------------------------------------------------- Clock

func TestP3_9_Evict(t *testing.T) {
	runEvictCases(t, func() Replacer { return NewClockReplacer(4) }, []evictCase{
		{"empty replacer", nil, 0, nil},
		{"only pinned frames", access(0, 1, 2), 0, nil},
		{"doc example", script(access(0, 1, 2, 3, 1), evictable(0, 1, 2, 3)), 4, []FrameID{0, 2, 3, 1}},
		{"single frame", script(access(2, 2, 2), evictable(2)), 1, []FrameID{2}},
		{"pinned frames are skipped", script(access(0, 1, 2, 3), evictable(1, 3)), 2, []FrameID{1, 3}},
		{"removed frame is never evicted", script(access(0, 1, 2), evictable(0, 1, 2), []op{{'r', 0}}), 2, []FrameID{1, 2}},
	})
}

func TestP3_9_Evict_HandPosition(t *testing.T) {
	r := NewClockReplacer(4)
	run(r, script(access(0, 1, 2, 3, 1), evictable(0, 1, 2, 3)))
	steps := []struct {
		victim FrameID
		hand   int
		usage  []int
	}{
		{0, 1, []int{0, 1, 0, 0}},
		{2, 3, []int{0, 0, 0, 0}},
		{3, 0, []int{0, 0, 0, 0}},
		{1, 2, []int{0, 0, 0, 0}},
	}
	for i, s := range steps {
		f, ok := r.Evict()
		if !ok || f != s.victim {
			t.Fatalf("evict #%d = %d, %v; want %d", i+1, f, ok, s.victim)
		}
		if r.hand != s.hand {
			t.Fatalf("after evict #%d hand = %d, want %d", i+1, r.hand, s.hand)
		}
		if !slices.Equal(r.usage, s.usage) {
			t.Fatalf("after evict #%d usage = %v, want %v", i+1, r.usage, s.usage)
		}
	}
	if _, ok := r.Evict(); ok || r.hand != 2 {
		t.Fatalf("Evict on empty: ok=%v hand=%d, want false and hand unchanged (2)", ok, r.hand)
	}
}

// A frame re-used after the hand passed it gets a second chance.
func TestP3_9_Evict_SecondChance(t *testing.T) {
	r := NewClockReplacer(3)
	run(r, script(access(0, 1, 2), evictable(0, 1, 2)))
	// sweep: f0 1->0, f1 1->0, f2 1->0, f0 victim; hand=1
	if f, _ := r.Evict(); f != 0 {
		t.Fatalf("first victim = %d, want 0", f)
	}
	r.RecordAccess(1) // usage 0 -> 1: second chance
	// hand=1: f1 1->0, f2 is 0 -> victim 2
	if f, _ := r.Evict(); f != 2 {
		t.Fatalf("second victim = %d, want 2 (frame 1 was just used)", f)
	}
	if f, _ := r.Evict(); f != 1 {
		t.Fatalf("third victim = %d, want 1", f)
	}
}

// Pinned buffers keep their usage count: the hand only decrements unpinned ones.
func TestP3_9_Evict_PinnedNotDecremented(t *testing.T) {
	r := NewClockReplacer(2)
	run(r, script(access(0, 0, 0, 1), evictable(1)))
	if f, ok := r.Evict(); !ok || f != 1 {
		t.Fatalf("Evict() = %d, %v; want 1", f, ok)
	}
	if r.usage[0] != 3 {
		t.Fatalf("pinned frame 0 usage = %d, want 3 (unchanged)", r.usage[0])
	}
	if r.hand != 0 {
		t.Fatalf("hand = %d, want 0", r.hand)
	}
}

// Frame 0 used 10 times, frame 1 used 5 times: both are capped at 5, so the
// hand (starting at 0) drains them evenly and frame 0 reaches 0 first.
func TestP3_9_Evict_UsageCapped(t *testing.T) {
	r := NewClockReplacer(2)
	for i := 0; i < 10; i++ {
		r.RecordAccess(0)
	}
	for i := 0; i < 5; i++ {
		r.RecordAccess(1)
	}
	run(r, evictable(0, 1))
	if f, _ := r.Evict(); f != 0 {
		t.Fatalf("victim = %d, want 0 (usage counts are capped at %d)", f, MaxUsageCount)
	}
	if r.usage[1] != 0 {
		t.Fatalf("frame 1 usage = %d, want 0", r.usage[1])
	}
}

// ---------------------------------------------------------------- LRU-K

func TestP3_10_Evict(t *testing.T) {
	k2 := func() Replacer { return NewLRUKReplacer(8, 2) }
	runEvictCases(t, k2, []evictCase{
		{"empty replacer", nil, 0, nil},
		{"only pinned frames", access(0, 0, 1), 0, nil},
		{"doc example: +inf first, then largest k-distance", script(access(0, 1, 2, 0, 1), evictable(0, 1, 2)), 3, []FrameID{2, 0, 1}},
		{"k-distance beats recency", script(access(0, 1, 1, 0), evictable(0, 1)), 2, []FrameID{0, 1}},
		{"scan page loses to hot page", script(access(0, 0, 1), evictable(0, 1)), 2, []FrameID{1, 0}},
		{"all +inf: FIFO by first access", script(access(3, 1, 2), evictable(1, 2, 3)), 3, []FrameID{3, 1, 2}},
		{"pinned frames are skipped", script(access(0, 1, 2, 0, 1, 2), evictable(1, 2)), 2, []FrameID{1, 2}},
		{"removed frame is never evicted", script(access(0, 1, 2), evictable(0, 1, 2), []op{{'r', 0}}), 2, []FrameID{1, 2}},
	})
	k3 := func() Replacer { return NewLRUKReplacer(8, 3) }
	runEvictCases(t, k3, []evictCase{
		{"K=3: +inf FIFO by first access, not last", script(access(0, 1, 0), evictable(0, 1)), 2, []FrameID{0, 1}},
		{"K=3: three accesses make a frame finite", script(access(0, 1, 0, 0), evictable(0, 1)), 2, []FrameID{1, 0}},
	})
	k1 := func() Replacer { return NewLRUKReplacer(8, 1) }
	runEvictCases(t, k1, []evictCase{
		{"K=1 is plain LRU", script(access(0, 1, 2, 0), evictable(0, 1, 2)), 3, []FrameID{1, 2, 0}},
	})
}

func TestP3_10_Evict_HistoryResetAfterEvict(t *testing.T) {
	r := NewLRUKReplacer(4, 2)
	run(r, script(access(0, 0, 1), evictable(0, 1))) // f0=[0 1] f1=[2]
	if f, _ := r.Evict(); f != 1 {
		t.Fatalf("victim = %d, want 1", f)
	}
	if _, ok := r.history[1]; ok {
		t.Fatal("evicted frame 1 still has history")
	}
	run(r, script(access(1), evictable(1))) // f1=[3] — +inf again
	if f, _ := r.Evict(); f != 1 {
		t.Fatalf("victim = %d, want 1 (fresh single access is +inf)", f)
	}
	if r.Size() != 1 {
		t.Fatalf("Size() = %d, want 1", r.Size())
	}
	run(r, access(2, 2)) // f2=[4 5], pinned
	if f, _ := r.Evict(); f != 0 {
		t.Fatalf("victim = %d, want 0", f)
	}
	if _, ok := r.Evict(); ok {
		t.Fatal("Evict succeeded, want false (only pinned frame 2 left)")
	}
}

// Only the last K accesses count: f0's old access at t=0 is forgotten.
func TestP3_10_Evict_OnlyLastKAccessesCount(t *testing.T) {
	r := NewLRUKReplacer(4, 2)
	// t: 0  1  2  3  4  5
	//    f0 f1 f1 f0 f0 f1  -> f0=[3 4], f1=[2 5]
	run(r, script(access(0, 1, 1, 0, 0, 1), evictable(0, 1)))
	if f, _ := r.Evict(); f != 1 {
		t.Fatalf("victim = %d, want 1 (k-th most recent: f0@3, f1@2)", f)
	}
}
