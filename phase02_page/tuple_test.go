package page

import (
	"bytes"
	"errors"
	"testing"
)

type slotData struct {
	slot SlotID
	data string
}

func collect(p *Page) []slotData {
	var out []slotData
	for s, d := range p.Tuples() {
		out = append(out, slotData{s, string(d)})
	}
	return out
}

func TestP2_6_Tuples(t *testing.T) {
	tests := []struct {
		name string
		page *Page
		want []slotData
	}{
		{"empty page yields nothing", NewPage(), nil},
		{"dead slot is skipped",
			mkPage(t, lp{LPNormal, "hello"}, lp{LPDead, "db"}, lp{LPNormal, "xyz"}),
			[]slotData{{0, "hello"}, {2, "xyz"}}},
		{"unused and redirect are skipped",
			mkPage(t, lp{}, lp{LPNormal, "a"}, lp{LPRedirect, "r"}, lp{}, lp{LPNormal, "bb"}),
			[]slotData{{1, "a"}, {4, "bb"}}},
		{"only dead slots", mkPage(t, lp{LPDead, "x"}, lp{LPDead, "y"}), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := collect(tc.page)
			if len(got) != len(tc.want) {
				t.Fatalf("Tuples() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Tuples() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestP2_6_Tuples_BreakStopsEarly(t *testing.T) {
	p := mkPage(t, lp{LPNormal, "a"}, lp{LPNormal, "b"}, lp{LPNormal, "c"})
	var seen []SlotID
	for s := range p.Tuples() {
		seen = append(seen, s)
		if s == 1 {
			break
		}
	}
	if len(seen) != 2 || seen[0] != 0 || seen[1] != 1 {
		t.Fatalf("visited %v, want [0 1] then stop", seen)
	}
}

func TestP2_6_Tuples_AfterInserts(t *testing.T) {
	p := NewPage()
	for _, s := range []string{"one", "two", "three"} {
		if _, err := p.InsertTuple([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	got := collect(p)
	want := []slotData{{0, "one"}, {1, "two"}, {2, "three"}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("Tuples() = %v, want %v", got, want)
	}
}

func TestP2_7_DeleteTuple(t *testing.T) {
	p := NewPage()
	p.InsertTuple([]byte("hello"))
	p.InsertTuple([]byte("db"))
	free := p.FreeSpace()
	before := snapshot(p)

	if err := p.DeleteTuple(1); err != nil {
		t.Fatalf("DeleteTuple(1) = %v, want nil", err)
	}
	if got := rawLP(p, 1); !bytes.Equal(got, []byte{0xF9, 0x1F, 0x02, 0xC0}) {
		t.Fatalf("lp1 bytes = % X, want F9 1F 02 C0 (only flags flip to DEAD)", got)
	}
	// Only byte 15 (high byte of lp1's word) may change.
	after := snapshot(p)
	after[15] = before[15]
	if after != before {
		t.Fatalf("DeleteTuple changed bytes other than lp1's flag bits")
	}
	if _, err := p.GetTuple(1); !errors.Is(err, ErrInvalidSlot) {
		t.Fatalf("GetTuple(1) after delete err = %v, want ErrInvalidSlot", err)
	}
	if got, _ := p.GetTuple(0); string(got) != "hello" {
		t.Fatalf("GetTuple(0) = %q, want hello", got)
	}
	if string(p.buf[8185:8187]) != "db" {
		t.Fatalf("tuple bytes were erased; delete must leave them for Compact")
	}
	if p.FreeSpace() != free {
		t.Fatalf("FreeSpace = %d after delete, want unchanged %d", p.FreeSpace(), free)
	}
	// a new insert gets a new slot, never the dead one
	if s, err := p.InsertTuple([]byte("new")); err != nil || s != 2 {
		t.Fatalf("InsertTuple after delete = slot %d, %v; want 2", s, err)
	}
}

func TestP2_7_DeleteTuple_Errors(t *testing.T) {
	p := mkPage(t, lp{LPNormal, "a"}, lp{LPDead, "b"}, lp{}, lp{LPRedirect, "r"})
	before := snapshot(p)
	for _, tc := range []struct {
		name string
		slot SlotID
	}{
		{"already dead", 1},
		{"unused", 2},
		{"redirect", 3},
		{"past the array", 4},
	} {
		if err := p.DeleteTuple(tc.slot); !errors.Is(err, ErrInvalidSlot) {
			t.Fatalf("%s: DeleteTuple(%d) = %v, want ErrInvalidSlot", tc.name, tc.slot, err)
		}
		assertUnchanged(t, p, before)
	}
}

func TestP2_8_UpdateTuple(t *testing.T) {
	newPage := func() *Page {
		// slot 0 "aaaa" @8188, slot 1 "bbbbbbbb" @8180, slot 2 "cc" @8178
		return mkPage(t, lp{LPNormal, "aaaa"}, lp{LPNormal, "bbbbbbbb"}, lp{LPNormal, "cc"})
	}
	tests := []struct {
		name    string
		slot    SlotID
		data    string
		wantErr error
		wantLP  ItemID
	}{
		{"shrink keeps the offset", 1, "BB", nil, ItemID{8180, LPNormal, 2}},
		{"same size", 1, "12345678", nil, ItemID{8180, LPNormal, 8}},
		{"one byte too long", 1, "123456789", ErrNoRoom, ItemID{}},
		{"grow last tuple", 2, "ccc", ErrNoRoom, ItemID{}},
		{"empty data", 0, "", ErrEmptyTuple, ItemID{}},
		{"slot past the array", 3, "x", ErrInvalidSlot, ItemID{}},
		{"invalid slot wins over empty data", 3, "", ErrInvalidSlot, ItemID{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPage()
			before := snapshot(p)
			err := p.UpdateTuple(tc.slot, []byte(tc.data))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				assertUnchanged(t, p, before)
				return
			}
			if it, _ := p.ItemID(tc.slot); it != tc.wantLP {
				t.Fatalf("lp%d = %+v, want %+v", tc.slot, it, tc.wantLP)
			}
			if got, _ := p.GetTuple(tc.slot); string(got) != tc.data {
				t.Fatalf("GetTuple = %q, want %q", got, tc.data)
			}
			if p.Lower() != 20 || p.Upper() != 8178 {
				t.Fatalf("lower/upper = %d/%d, want 20/8178 unchanged", p.Lower(), p.Upper())
			}
			for _, other := range []struct {
				s    SlotID
				want string
			}{{0, "aaaa"}, {1, "bbbbbbbb"}, {2, "cc"}} {
				if other.s == tc.slot {
					continue
				}
				if got, _ := p.GetTuple(other.s); string(got) != other.want {
					t.Fatalf("neighbour slot %d = %q, want %q", other.s, got, other.want)
				}
			}
		})
	}
}

func TestP2_8_UpdateTuple_ShrinkLeavesHoleThenRejectsRegrow(t *testing.T) {
	p := mkPage(t, lp{LPNormal, "aaaa"}, lp{LPNormal, "bbbbbbbb"})
	if err := p.UpdateTuple(1, []byte("BB")); err != nil {
		t.Fatal(err)
	}
	if got := string(p.buf[8180:8188]); got != "BBbbbbbb" {
		t.Fatalf("bytes [8180,8188) = %q, want %q (leftover bytes stay)", got, "BBbbbbbb")
	}
	if err := p.UpdateTuple(1, []byte("ccc")); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("regrow to 3 bytes: err = %v, want ErrNoRoom (lp_len is now 2)", err)
	}
}

func TestP2_8_UpdateTuple_NonNormalSlots(t *testing.T) {
	p := mkPage(t, lp{LPDead, "dead"}, lp{}, lp{LPRedirect, "r"})
	before := snapshot(p)
	for s := SlotID(0); s < 3; s++ {
		if err := p.UpdateTuple(s, []byte("x")); !errors.Is(err, ErrInvalidSlot) {
			t.Fatalf("UpdateTuple(%d) = %v, want ErrInvalidSlot", s, err)
		}
	}
	assertUnchanged(t, p, before)
}
