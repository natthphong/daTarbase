package page

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestP2_5_InsertTuple(t *testing.T) {
	tests := []struct {
		name      string
		inserts   [][]byte
		wantSlots []SlotID // slots returned by successful inserts
		wantErr   error    // error expected on the LAST insert (nil = all succeed)
		wantLower uint16
		wantUpper uint16
	}{
		{
			name:      "single tuple",
			inserts:   [][]byte{[]byte("hello")},
			wantSlots: []SlotID{0},
			wantLower: 12, wantUpper: 8187,
		},
		{
			name:      "two tuples grow toward each other",
			inserts:   [][]byte{[]byte("hello"), []byte("db")},
			wantSlots: []SlotID{0, 1},
			wantLower: 16, wantUpper: 8185,
		},
		{
			name:      "empty tuple is rejected",
			inserts:   [][]byte{{}},
			wantErr:   ErrEmptyTuple,
			wantLower: 8, wantUpper: 8192,
		},
		{
			name:      "nil tuple is rejected",
			inserts:   [][]byte{nil},
			wantErr:   ErrEmptyTuple,
			wantLower: 8, wantUpper: 8192,
		},
		{
			name:      "exact fit fills the page completely",
			inserts:   [][]byte{make([]byte, PageSize-HeaderSize-LinePointerLen)}, // 8180 bytes
			wantSlots: []SlotID{0},
			wantLower: 12, wantUpper: 12,
		},
		{
			name:      "one byte too big leaves page unchanged",
			inserts:   [][]byte{make([]byte, PageSize-HeaderSize-LinePointerLen+1)}, // 8181 bytes
			wantErr:   ErrPageFull,
			wantLower: 8, wantUpper: 8192,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPage()
			var gotSlots []SlotID
			var err error
			for i, data := range tc.inserts {
				var s SlotID
				s, err = p.InsertTuple(data)
				if err != nil {
					if i != len(tc.inserts)-1 {
						t.Fatalf("insert #%d: unexpected error %v", i, err)
					}
					break
				}
				gotSlots = append(gotSlots, s)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(gotSlots) != len(tc.wantSlots) {
				t.Fatalf("slots = %v, want %v", gotSlots, tc.wantSlots)
			}
			for i := range gotSlots {
				if gotSlots[i] != tc.wantSlots[i] {
					t.Fatalf("slots = %v, want %v", gotSlots, tc.wantSlots)
				}
			}
			if p.Lower() != tc.wantLower || p.Upper() != tc.wantUpper {
				t.Fatalf("lower/upper = %d/%d, want %d/%d", p.Lower(), p.Upper(), tc.wantLower, tc.wantUpper)
			}
		})
	}
}

// Checks the exact on-disk bytes, not just the API.
func TestP2_5_InsertTuple_ByteLayout(t *testing.T) {
	p := NewPage()
	p.InsertTuple([]byte("hello"))
	p.InsertTuple([]byte("db"))
	b := p.Bytes()

	// line pointer 0 at [8,12): offset=8187, word = NORMAL<<14 | 5 = 0x4005
	if off, w := binary.LittleEndian.Uint16(b[8:]), binary.LittleEndian.Uint16(b[10:]); off != 8187 || w != 0x4005 {
		t.Errorf("lp0 = (off %d, word %#x), want (8187, 0x4005)", off, w)
	}
	// line pointer 1 at [12,16): offset=8185, word = NORMAL<<14 | 2 = 0x4002
	if got := b[12:16]; !bytes.Equal(got, []byte{0xF9, 0x1F, 0x02, 0x40}) {
		t.Errorf("lp1 bytes = % X, want F9 1F 02 40", got)
	}
	if got := b[8185:8192]; !bytes.Equal(got, []byte("dbhello")) {
		t.Errorf("tuple area = %q, want %q", got, "dbhello")
	}
	if got := b[16:8185]; !bytes.Equal(got, make([]byte, len(got))) {
		t.Errorf("free hole [16,8185) is not all zero")
	}
}

// Fill with 100-byte tuples: each costs 104 bytes, (8192-8)/104 = 78 fit.
func TestP2_5_InsertTuple_FillUntilFull(t *testing.T) {
	p := NewPage()
	tuple := bytes.Repeat([]byte{0xAB}, 100)
	n := 0
	for {
		if _, err := p.InsertTuple(tuple); err != nil {
			if !errors.Is(err, ErrPageFull) {
				t.Fatalf("unexpected error: %v", err)
			}
			break
		}
		n++
	}
	if n != 78 {
		t.Fatalf("inserted %d tuples, want 78", n)
	}
	for s := SlotID(0); s < 78; s++ {
		got, err := p.GetTuple(s)
		if err != nil || !bytes.Equal(got, tuple) {
			t.Fatalf("slot %d: got %x err %v", s, got, err)
		}
	}
}

func TestP2_5_InsertTuple_ReusesUnusedSlot(t *testing.T) {
	// slots: 0 NORMAL "aaaa", 1 UNUSED, 2 NORMAL "bb"  → lower=20, upper=8186
	p := mkPage(t, lp{LPNormal, "aaaa"}, lp{}, lp{LPNormal, "bb"})

	s, err := p.InsertTuple([]byte("zzz"))
	if err != nil || s != 1 {
		t.Fatalf("insert zzz = slot %d, %v; want slot 1 (lowest unused)", s, err)
	}
	if p.Lower() != 20 || p.Upper() != 8183 {
		t.Fatalf("lower/upper = %d/%d, want 20/8183 (no new line pointer)", p.Lower(), p.Upper())
	}
	if got := rawLP(p, 1); !bytes.Equal(got, []byte{0xF7, 0x1F, 0x03, 0x40}) {
		t.Fatalf("lp1 bytes = % X, want F7 1F 03 40 (off 8183, NORMAL, len 3)", got)
	}

	s, err = p.InsertTuple([]byte("q"))
	if err != nil || s != 3 {
		t.Fatalf("insert q = slot %d, %v; want slot 3 (appended)", s, err)
	}
	if p.Lower() != 24 || p.Upper() != 8182 {
		t.Fatalf("lower/upper = %d/%d, want 24/8182", p.Lower(), p.Upper())
	}
	for slot, want := range []string{"aaaa", "zzz", "bb", "q"} {
		if got, err := p.GetTuple(SlotID(slot)); err != nil || string(got) != want {
			t.Fatalf("GetTuple(%d) = %q, %v; want %q", slot, got, err, want)
		}
	}
}

func TestP2_5_InsertTuple_PicksLowestUnusedAndSkipsDead(t *testing.T) {
	p := mkPage(t, lp{LPDead, "dd"}, lp{LPNormal, "n"}, lp{}, lp{}, lp{LPNormal, "m"})
	for _, want := range []SlotID{2, 3, 5} {
		s, err := p.InsertTuple([]byte("x"))
		if err != nil || s != want {
			t.Fatalf("InsertTuple = slot %d, %v; want slot %d (dead slot 0 must never be reused)", s, err, want)
		}
	}
	if it, _ := p.ItemID(0); it.Flags != LPDead {
		t.Fatalf("slot 0 flags = %v, want DEAD untouched", it.Flags)
	}
}

func TestP2_5_InsertTuple_ExactFitWithReusedSlot(t *testing.T) {
	// hole = upper-lower = 8192-8000 - 16 = 176; slot 1 is unused, so all 176 bytes are usable.
	p := mkPage(t, lp{LPNormal, repeat('x', 8000)}, lp{})
	before := snapshot(p)
	if _, err := p.InsertTuple(make([]byte, 177)); !errors.Is(err, ErrPageFull) {
		t.Fatalf("177 bytes: err = %v, want ErrPageFull", err)
	}
	assertUnchanged(t, p, before)
	s, err := p.InsertTuple(bytes.Repeat([]byte{'y'}, 176))
	if err != nil || s != 1 {
		t.Fatalf("176 bytes: slot %d, %v; want slot 1", s, err)
	}
	if p.Lower() != 16 || p.Upper() != 16 {
		t.Fatalf("lower/upper = %d/%d, want 16/16", p.Lower(), p.Upper())
	}
}

func TestP2_5_InsertTuple_ErrorLeavesPageUnchanged(t *testing.T) {
	p := mkPage(t, lp{LPNormal, repeat('x', 8170)}) // lower=12 upper=22, FreeSpace=6
	before := snapshot(p)
	for _, data := range [][]byte{nil, make([]byte, 7)} {
		if _, err := p.InsertTuple(data); err == nil {
			t.Fatalf("InsertTuple(%d bytes) succeeded, want error", len(data))
		}
		assertUnchanged(t, p, before)
	}
	if s, err := p.InsertTuple([]byte("123456")); err != nil || s != 1 {
		t.Fatalf("6-byte exact fit = slot %d, %v; want slot 1", s, err)
	}
}
