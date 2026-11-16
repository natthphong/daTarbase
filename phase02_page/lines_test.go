package page

import (
	"bytes"
	"errors"
	"testing"
)

func TestP2_1_SetItemID(t *testing.T) {
	tests := []struct {
		name    string
		slot    SlotID
		it      ItemID
		wantErr error
		wantAt  int    // byte offset of the line pointer
		want    []byte // expected 4 bytes on success
	}{
		{"normal tuple in slot 1", 1, ItemID{Off: 8185, Flags: LPNormal, Len: 2}, nil, 12, []byte{0xF9, 0x1F, 0x02, 0x40}},
		{"dead tuple in slot 0", 0, ItemID{Off: 8187, Flags: LPDead, Len: 5}, nil, 8, []byte{0xFB, 0x1F, 0x05, 0xC0}},
		{"unused is all zero", 0, ItemID{}, nil, 8, []byte{0, 0, 0, 0}},
		{"redirect with max length", 1, ItemID{Off: 0x1234, Flags: LPRedirect, Len: MaxItemLen}, nil, 12, []byte{0x34, 0x12, 0xFF, 0xBF}},
		{"slot past the array", 2, ItemID{Off: 8000, Flags: LPNormal, Len: 1}, ErrInvalidSlot, 0, nil},
		{"length needs 15 bits", 0, ItemID{Len: 16384}, ErrBadItemID, 0, nil},
		{"flags out of range", 0, ItemID{Flags: 4, Len: 1}, ErrBadItemID, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPage()
			p.setLower(16) // two slots, both zero
			p.buf[100] = 0x77
			before := snapshot(p)
			err := p.SetItemID(tc.slot, tc.it)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				assertUnchanged(t, p, before)
				return
			}
			if got := p.buf[tc.wantAt : tc.wantAt+4]; !bytes.Equal(got, tc.want) {
				t.Fatalf("bytes [%d,%d) = % X, want % X", tc.wantAt, tc.wantAt+4, got, tc.want)
			}
			// nothing else changed
			copy(p.buf[tc.wantAt:], before[tc.wantAt:tc.wantAt+4])
			assertUnchanged(t, p, before)
		})
	}
}

func TestP2_2_ItemID(t *testing.T) {
	p := NewPage()
	p.setLower(20)
	copy(p.buf[8:], []byte{0xFB, 0x1F, 0x05, 0xC0})  // slot 0: DEAD off 8187 len 5
	copy(p.buf[12:], []byte{0xF9, 0x1F, 0x02, 0x40}) // slot 1: NORMAL off 8185 len 2
	copy(p.buf[16:], []byte{0x00, 0x00, 0x00, 0x00}) // slot 2: UNUSED
	p.buf[20] = 0xFF                                 // beyond lower: not a slot

	tests := []struct {
		slot    SlotID
		want    ItemID
		wantErr error
	}{
		{0, ItemID{Off: 8187, Flags: LPDead, Len: 5}, nil},
		{1, ItemID{Off: 8185, Flags: LPNormal, Len: 2}, nil},
		{2, ItemID{}, nil},
		{3, ItemID{}, ErrInvalidSlot},
		{65535, ItemID{}, ErrInvalidSlot},
	}
	for _, tc := range tests {
		got, err := p.ItemID(tc.slot)
		if !errors.Is(err, tc.wantErr) || got != tc.want {
			t.Errorf("ItemID(%d) = %+v, %v; want %+v, %v", tc.slot, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestP2_2_ItemID_RoundTrip(t *testing.T) {
	p := NewPage()
	p.setLower(12)
	for _, want := range []ItemID{
		{Off: 8185, Flags: LPNormal, Len: 2},
		{Off: 65535, Flags: LPRedirect, Len: MaxItemLen},
		{Off: 9, Flags: LPDead, Len: 1},
		{},
	} {
		if err := p.SetItemID(0, want); err != nil {
			t.Fatalf("SetItemID(%+v): %v", want, err)
		}
		if got, err := p.ItemID(0); err != nil || got != want {
			t.Fatalf("ItemID after SetItemID(%+v) = %+v, %v", want, got, err)
		}
	}
}

func TestP2_3_FreeSpace(t *testing.T) {
	tests := []struct {
		name string
		page *Page
		want int
	}{
		{"empty page needs a new line pointer", NewPage(), 8180},
		{"two normal slots", mkPage(t, lp{LPNormal, "hello"}, lp{LPNormal, "db"}), 8165},
		{"unused slot can be recycled", mkPage(t, lp{LPNormal, "aaaa"}, lp{}, lp{LPNormal, "bb"}), 8166},
		{"dead slot is not reusable", mkPage(t, lp{LPNormal, "aaaa"}, lp{LPDead, "bb"}), 8192 - 6 - 16 - 4},
		{"hole smaller than a line pointer clamps to 0", mkPage(t, lp{LPNormal, repeat('x', 8000)}, lp{LPDead, repeat('y', 174)}), 0},
		{"hole of exactly 4 bytes", mkPage(t, lp{LPNormal, repeat('x', 8176)}), 0},
		{"hole of 5 bytes leaves 1", mkPage(t, lp{LPNormal, repeat('x', 8175)}), 1},
		{"full page with an unused slot", mkPage(t, lp{LPNormal, repeat('x', 8176)}, lp{}), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := snapshot(tc.page)
			if got := tc.page.FreeSpace(); got != tc.want {
				t.Fatalf("FreeSpace() = %d (lower=%d upper=%d), want %d", got, tc.page.Lower(), tc.page.Upper(), tc.want)
			}
			assertUnchanged(t, tc.page, before)
		})
	}
}

func TestP2_4_GetTuple(t *testing.T) {
	p := mkPage(t,
		lp{LPNormal, "hello"},
		lp{LPNormal, "db"},
		lp{LPDead, "gone"},
		lp{},
		lp{LPRedirect, "r"},
	)
	tests := []struct {
		slot    SlotID
		want    string
		wantErr error
	}{
		{0, "hello", nil},
		{1, "db", nil},
		{2, "", ErrInvalidSlot}, // dead
		{3, "", ErrInvalidSlot}, // unused
		{4, "", ErrInvalidSlot}, // redirect
		{5, "", ErrInvalidSlot}, // past the array
	}
	for _, tc := range tests {
		got, err := p.GetTuple(tc.slot)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("GetTuple(%d) err = %v, want %v", tc.slot, err, tc.wantErr)
		}
		if tc.wantErr != nil {
			if got != nil {
				t.Fatalf("GetTuple(%d) = %q, want nil on error", tc.slot, got)
			}
			continue
		}
		if string(got) != tc.want {
			t.Fatalf("GetTuple(%d) = %q, want %q", tc.slot, got, tc.want)
		}
	}
}

func TestP2_4_GetTuple_AliasesPageWithCappedSlice(t *testing.T) {
	p := mkPage(t, lp{LPNormal, "hello"}, lp{LPNormal, "db"})
	db, err := p.GetTuple(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(db) != 2 || cap(db) != 2 {
		t.Fatalf("len/cap = %d/%d, want 2/2 (full slice expression)", len(db), cap(db))
	}
	db[0] = 'D' // zero copy: writes go to the page
	if p.buf[8185] != 'D' {
		t.Fatalf("page byte 8185 = %q, want 'D' (GetTuple must alias the page)", p.buf[8185])
	}
	_ = append(db, 'X') // must reallocate, not overwrite "hello"
	if got, _ := p.GetTuple(0); string(got) != "hello" {
		t.Fatalf("slot 0 = %q after append to slot 1, want %q", got, "hello")
	}
}
