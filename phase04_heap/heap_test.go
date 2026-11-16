package heap

import (
	"bytes"
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP4_5_Insert(t *testing.T) {
	h := NewHeap()
	steps := []struct {
		name     string
		size     int
		want     types.TID
		page0Cat uint8
		reads    int // cumulative store.Reads after the step
	}{
		{"first insert extends page 0", 4000, tid(0, 0), 130, 0},
		{"FSM says page 0 has cat 130 ≥ 126", 4000, tid(0, 1), 5, 1},
		{"no page has room: extend page 1", 4000, tid(1, 0), 5, 1},
		{"small tuple goes back to page 0", 100, tid(0, 2), 2, 2},
	}
	for _, s := range steps {
		got, err := h.Insert(fill(s.size, 'x'))
		if err != nil {
			t.Fatalf("%s: unexpected error %v", s.name, err)
		}
		if got != s.want {
			t.Fatalf("%s: TID = %v, want %v", s.name, got, s.want)
		}
		if c := h.FSM.Category(0); c != s.page0Cat {
			t.Fatalf("%s: FSM page 0 = %d, want %d", s.name, c, s.page0Cat)
		}
		if h.Store.Reads != s.reads {
			t.Fatalf("%s: Reads = %d, want %d", s.name, h.Store.Reads, s.reads)
		}
	}
	if c := h.FSM.Category(1); c != 130 {
		t.Fatalf("FSM page 1 = %d, want 130", c)
	}
	if p := h.Store.Peek(0); p.FreeSpace() != 72 || p.NumSlots() != 3 {
		t.Fatalf("page 0 free/slots = %d/%d, want 72/3", p.FreeSpace(), p.NumSlots())
	}
	if !bytes.Equal(h.Store.Peek(0).Slots[2], fill(100, 'x')) {
		t.Fatalf("page 0 slot 2 holds the wrong bytes")
	}
}

func TestP4_5_Insert_Errors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"empty tuple", []byte{}, ErrEmptyTuple},
		{"nil tuple", nil, ErrEmptyTuple},
		{"one byte too big", fill(MaxTupleSize+1, 'z'), ErrTupleTooBig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHeap()
			h.Insert([]byte("keep"))
			_, err := h.Insert(tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if h.Store.NumPages() != 1 || h.Store.Peek(0).NumSlots() != 1 || h.FSM.Len() != 1 {
				t.Fatalf("failed insert changed state: pages=%d slots=%d fsm=%d",
					h.Store.NumPages(), h.Store.Peek(0).NumSlots(), h.FSM.Len())
			}
		})
	}
}

func TestP4_5_Insert_ExactFit(t *testing.T) {
	h := NewHeap()
	got, err := h.Insert(fill(MaxTupleSize, 'a'))
	if err != nil || got != tid(0, 0) {
		t.Fatalf("Insert(8180 bytes) = %v, %v; want (0,0), nil", got, err)
	}
	if h.Store.Peek(0).FreeSpace() != 0 || h.FSM.Category(0) != 0 {
		t.Fatalf("page should be full: free=%d cat=%d", h.Store.Peek(0).FreeSpace(), h.FSM.Category(0))
	}
	got, _ = h.Insert([]byte("b"))
	if got != tid(1, 0) || h.Store.Reads != 0 {
		t.Fatalf("next insert = %v with %d reads, want (1,0) with 0 reads", got, h.Store.Reads)
	}
}

func TestP4_5_Insert_StaleFSM(t *testing.T) {
	t.Run("page filled behind the FSM's back", func(t *testing.T) {
		h := NewHeap()
		h.Insert(fill(100, 'a')) // (0,0), free 8080 → cat 252
		h.Store.Peek(0).Add(fill(8000, 'b'))
		// page 0 really has 76 bytes free now, FSM still says 252
		got, err := h.Insert(fill(200, 'c'))
		if err != nil || got != tid(1, 0) {
			t.Fatalf("Insert = %v, %v; want (1,0), nil", got, err)
		}
		if h.Store.Reads != 1 {
			t.Fatalf("Reads = %d, want 1 (one look at the stale page)", h.Store.Reads)
		}
		if c := h.FSM.Category(0); c != 2 {
			t.Fatalf("FSM page 0 = %d, want 2 (76 bytes, corrected)", c)
		}
		if c := h.FSM.Category(1); c != 249 {
			t.Fatalf("FSM page 1 = %d, want 249 (7980 bytes)", c)
		}
	})
	t.Run("FSM points past the end of the file", func(t *testing.T) {
		h := NewHeap()
		h.FSM.Update(5, 8000)
		got, err := h.Insert([]byte("x"))
		if err != nil || got != tid(0, 0) {
			t.Fatalf("Insert = %v, %v; want (0,0), nil", got, err)
		}
		if c := h.FSM.Category(5); c != 0 {
			t.Fatalf("FSM page 5 = %d, want 0 after correction", c)
		}
		if c := h.FSM.Category(0); c != 255 {
			t.Fatalf("FSM page 0 = %d, want 255", c)
		}
	})
	t.Run("stale first candidate, good second candidate", func(t *testing.T) {
		h := NewHeap()
		h.Store.Extend()
		_, p1 := h.Store.Extend()
		p1.Add(fill(1000, 'q'))
		h.Store.Peek(0).Add(fill(8170, 'f')) // page 0 really has 10 bytes free…
		h.FSM.Update(0, 8184)                // …but the FSM still thinks it is empty
		h.FSM.Update(1, p1.FreeSpace())
		got, err := h.Insert(fill(500, 'n'))
		if err != nil || got != tid(1, 1) {
			t.Fatalf("Insert = %v, %v; want (1,1), nil", got, err)
		}
		if h.Store.Reads != 2 || h.Store.NumPages() != 2 {
			t.Fatalf("Reads=%d pages=%d, want 2 reads, no extension", h.Store.Reads, h.Store.NumPages())
		}
	})
}

// fetchFixture:
//
//	page 0: (0,0) "a" live | (0,1) "old" →(0,2) | (0,2) "mid" →(2,0) | (0,3) "loop" →(2,9)
//	page 1: (1,0) "deleted" dead
//	page 2: (2,0) "new" live
func fetchFixture() *PageStore {
	s := mkStore([]string{"a", "old", "mid", "broken"}, []string{"deleted"}, []string{"new"})
	forward(s, tid(0, 1), tid(0, 2))
	forward(s, tid(0, 2), tid(2, 0))
	forward(s, tid(0, 3), tid(2, 9))
	kill(s, tid(1, 0))
	return s
}

func TestP4_6_Fetch(t *testing.T) {
	tests := []struct {
		name    string
		tid     types.TID
		data    string
		at      types.TID
		wantErr error
		reads   int
	}{
		{"live tuple", tid(0, 0), "a", tid(0, 0), nil, 1},
		{"live tuple on another page", tid(2, 0), "new", tid(2, 0), nil, 1},
		{"chain within page then across pages", tid(0, 1), "new", tid(2, 0), nil, 2},
		{"chain starting at second hop", tid(0, 2), "new", tid(2, 0), nil, 2},
		{"deleted tuple", tid(1, 0), "", types.TID{}, ErrTupleDead, 1},
		{"slot out of range", tid(0, 9), "", types.TID{}, ErrInvalidTID, 1},
		{"page out of range", tid(5, 0), "", types.TID{}, ErrInvalidTID, 0},
		{"chain points at a missing slot", tid(0, 3), "", types.TID{}, ErrInvalidTID, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &Heap{Store: fetchFixture(), FSM: NewFSM()}
			data, at, err := h.Fetch(tc.tid)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if string(data) != tc.data || at != tc.at {
				t.Fatalf("Fetch(%v) = %q at %v, want %q at %v", tc.tid, data, at, tc.data, tc.at)
			}
			if tc.wantErr != nil && data != nil {
				t.Fatalf("data must be nil on error")
			}
			if h.Store.Reads != tc.reads {
				t.Fatalf("Reads = %d, want %d", h.Store.Reads, tc.reads)
			}
		})
	}
}

func TestP4_7_Delete(t *testing.T) {
	h := NewHeap()
	a, _ := h.Insert([]byte("alice"))
	b, _ := h.Insert([]byte("bob"))
	usedBefore, catBefore := h.Store.Peek(0).Used, h.FSM.Category(0)
	h.Store.Reads = 0

	if err := h.Delete(a); err != nil {
		t.Fatalf("Delete(%v) = %v, want nil", a, err)
	}
	if h.Store.Reads != 1 {
		t.Fatalf("Reads = %d, want 1", h.Store.Reads)
	}
	p := h.Store.Peek(0)
	if !p.Dead[0] || p.Dead[1] {
		t.Fatalf("Dead = %v, want [true false]", p.Dead)
	}
	if p.Used != usedBefore || h.FSM.Category(0) != catBefore || string(p.Slots[0]) != "alice" || p.Forward[0] != InvalidTID {
		t.Fatalf("Delete must only set Dead (used %d→%d, cat %d→%d, slot %q, fwd %v)",
			usedBefore, p.Used, catBefore, h.FSM.Category(0), p.Slots[0], p.Forward[0])
	}

	tests := []struct {
		name string
		tid  types.TID
		want error
	}{
		{"delete twice", a, ErrTupleDead},
		{"slot out of range", tid(0, 7), ErrInvalidTID},
		{"page out of range", tid(3, 0), ErrInvalidTID},
		{"other tuple still deletable", b, nil},
	}
	for _, tc := range tests {
		if err := h.Delete(tc.tid); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Delete(%v) = %v, want %v", tc.name, tc.tid, err, tc.want)
		}
	}
	if _, _, err := h.Fetch(a); !errors.Is(err, ErrTupleDead) {
		t.Fatalf("Fetch(deleted) err = %v, want ErrTupleDead", err)
	}
}

func TestP4_7_Delete_DoesNotFollowChain(t *testing.T) {
	s := mkStore([]string{"v1", "v2"})
	forward(s, tid(0, 0), tid(0, 1))
	h := &Heap{Store: s, FSM: NewFSM()}
	if err := h.Delete(tid(0, 0)); !errors.Is(err, ErrTupleDead) {
		t.Fatalf("Delete(updated version) = %v, want ErrTupleDead", err)
	}
	if s.Peek(0).Dead[1] || s.Peek(0).Forward[0] != tid(0, 1) {
		t.Fatalf("Delete of an old version must not touch the chain")
	}
}

func TestP4_8_Update(t *testing.T) {
	h := NewHeap()
	v1, _ := h.Insert([]byte("v1"))        // (0,0)
	filler, _ := h.Insert(fill(5000, 'f')) // (0,1)
	if v1 != tid(0, 0) || filler != tid(0, 1) || h.Store.Peek(0).FreeSpace() != 3174 {
		t.Fatalf("setup: v1=%v filler=%v free=%d", v1, filler, h.Store.Peek(0).FreeSpace())
	}
	h.Store.Reads = 0

	v2, err := h.Update(v1, []byte("v2"))
	if err != nil || v2 != tid(0, 2) {
		t.Fatalf("HOT Update = %v, %v; want (0,2), nil", v2, err)
	}
	if h.Store.Reads != 1 {
		t.Fatalf("HOT update Reads = %d, want 1", h.Store.Reads)
	}
	p0 := h.Store.Peek(0)
	if !p0.Dead[0] || p0.Forward[0] != tid(0, 2) || p0.Dead[2] || p0.Forward[2] != InvalidTID {
		t.Fatalf("after HOT: Dead=%v Forward=%v", p0.Dead, p0.Forward)
	}
	if c := h.FSM.Category(0); c != 99 {
		t.Fatalf("FSM page 0 = %d, want 99 (3168 bytes)", c)
	}
	if data, at, _ := h.Fetch(v1); string(data) != "v2" || at != tid(0, 2) {
		t.Fatalf("Fetch(v1) = %q at %v, want \"v2\" at (0,2)", data, at)
	}

	if _, err := h.Update(v1, []byte("v3")); !errors.Is(err, ErrTupleDead) {
		t.Fatalf("Update(old version) err = %v, want ErrTupleDead", err)
	}

	big := fill(4000, 'B')
	v3, err := h.Update(v2, big)
	if err != nil || v3 != tid(1, 0) {
		t.Fatalf("cold Update = %v, %v; want (1,0), nil", v3, err)
	}
	if p0.Forward[2] != tid(1, 0) || !p0.Dead[2] {
		t.Fatalf("old version must forward to (1,0): Dead=%v Forward=%v", p0.Dead, p0.Forward)
	}
	h.Store.Reads = 0
	data, at, err := h.Fetch(v1)
	if err != nil || !bytes.Equal(data, big) || at != tid(1, 0) {
		t.Fatalf("Fetch(v1) = %d bytes at %v err %v, want 4000 bytes at (1,0)", len(data), at, err)
	}
	if h.Store.Reads != 2 {
		t.Fatalf("Fetch along (0,0)→(0,2)→(1,0): Reads = %d, want 2", h.Store.Reads)
	}
}

func TestP4_8_Update_UsesFSMForOtherPage(t *testing.T) {
	h := NewHeap()
	a, _ := h.Insert(fill(8000, 'a')) // (0,0), page 0 free 180
	h.Store.Extend()                  // page 1, empty
	h.FSM.Update(1, PageCapacity)
	h.Store.Reads = 0
	got, err := h.Update(a, fill(300, 'b'))
	if err != nil || got != tid(1, 0) {
		t.Fatalf("Update = %v, %v; want (1,0)", got, err)
	}
	if h.Store.NumPages() != 2 {
		t.Fatalf("NumPages = %d, want 2 (no extension)", h.Store.NumPages())
	}
	if h.Store.Reads != 2 {
		t.Fatalf("Reads = %d, want 2 (old page + FSM candidate)", h.Store.Reads)
	}
	if c := h.FSM.Category(1); c != 246 {
		t.Fatalf("FSM page 1 = %d, want 246 (7880 bytes)", c)
	}
}

func TestP4_8_Update_ErrorsLeaveStateUnchanged(t *testing.T) {
	tests := []struct {
		name string
		tid  types.TID
		data []byte
		want error
	}{
		{"empty data checked first", tid(9, 9), nil, ErrEmptyTuple},
		{"too big checked before tid", tid(9, 9), fill(MaxTupleSize+1, 'x'), ErrTupleTooBig},
		{"missing page", tid(9, 0), []byte("x"), ErrInvalidTID},
		{"missing slot", tid(0, 5), []byte("x"), ErrInvalidTID},
		{"deleted tuple", tid(0, 1), []byte("x"), ErrTupleDead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHeap()
			h.Insert([]byte("live"))
			h.Insert([]byte("gone"))
			h.Delete(tid(0, 1))
			_, err := h.Update(tc.tid, tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			p := h.Store.Peek(0)
			if h.Store.NumPages() != 1 || p.NumSlots() != 2 || p.Dead[0] || p.Forward[1] != InvalidTID {
				t.Fatalf("failed Update changed state: pages=%d slots=%d dead=%v fwd=%v",
					h.Store.NumPages(), p.NumSlots(), p.Dead, p.Forward)
			}
		})
	}
}
