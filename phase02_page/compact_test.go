package page

import (
	"bytes"
	"errors"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"
)

func mustInsert(t testing.TB, p *Page, data string) SlotID {
	t.Helper()
	s, err := p.InsertTuple([]byte(data))
	if err != nil {
		t.Fatalf("InsertTuple(%q): %v", data, err)
	}
	return s
}

func assertHoleZero(t testing.TB, p *Page) {
	t.Helper()
	hole := p.buf[p.Lower():p.Upper()]
	if !bytes.Equal(hole, make([]byte, len(hole))) {
		t.Fatalf("hole [%d,%d) is not all zero after Compact", p.Lower(), p.Upper())
	}
}

func TestP2_9_Compact(t *testing.T) {
	p := NewPage()
	for _, s := range []string{"aaaa", "bbbbbbbb", "cc", "ddd"} {
		mustInsert(t, p, s)
	}
	if p.Lower() != 24 || p.Upper() != 8175 {
		t.Fatalf("setup lower/upper = %d/%d, want 24/8175", p.Lower(), p.Upper())
	}
	p.DeleteTuple(1)
	p.DeleteTuple(3)

	if gained := p.Compact(); gained != 15 {
		t.Fatalf("Compact() = %d, want 15", gained)
	}
	want := []ItemID{
		{Off: 8188, Flags: LPNormal, Len: 4},
		{},
		{Off: 8186, Flags: LPNormal, Len: 2},
	}
	if p.NumSlots() != 3 || p.Lower() != 20 || p.Upper() != 8186 {
		t.Fatalf("slots/lower/upper = %d/%d/%d, want 3/20/8186", p.NumSlots(), p.Lower(), p.Upper())
	}
	for s, w := range want {
		if it, _ := p.ItemID(SlotID(s)); it != w {
			t.Fatalf("lp%d = %+v, want %+v", s, it, w)
		}
	}
	if got := string(p.buf[8186:8192]); got != "ccaaaa" {
		t.Fatalf("tuple area = %q, want %q", got, "ccaaaa")
	}
	if p.FreeSpace() != 8166 {
		t.Fatalf("FreeSpace = %d, want 8166", p.FreeSpace())
	}
	assertHoleZero(t, p)
	// the freed slot 1 is reused by the next insert
	if s := mustInsert(t, p, "e"); s != 1 {
		t.Fatalf("insert after compact = slot %d, want 1", s)
	}
}

func TestP2_9_Compact_Cases(t *testing.T) {
	tests := []struct {
		name       string
		page       func(t *testing.T) *Page
		wantGain   int
		wantLower  uint16
		wantUpper  uint16
		wantTuples []slotData
	}{
		{
			name:      "empty page",
			page:      func(t *testing.T) *Page { return NewPage() },
			wantGain:  0,
			wantLower: 8, wantUpper: 8192,
		},
		{
			name: "nothing to reclaim",
			page: func(t *testing.T) *Page {
				return mkPage(t, lp{LPNormal, "hello"}, lp{LPNormal, "db"})
			},
			wantGain:  0,
			wantLower: 16, wantUpper: 8185,
			wantTuples: []slotData{{0, "hello"}, {1, "db"}},
		},
		{
			name: "all dead resets the page",
			page: func(t *testing.T) *Page {
				return mkPage(t, lp{LPDead, "xx"}, lp{LPDead, "yyy"})
			},
			wantGain:  13, // hole 8171 → 8184
			wantLower: 8, wantUpper: 8192,
		},
		{
			name: "unused below live slot stays, trailing unused is cut",
			page: func(t *testing.T) *Page {
				return mkPage(t, lp{}, lp{LPDead, "dd"}, lp{LPNormal, "n"}, lp{}, lp{})
			},
			wantGain:  2 + 8, // dead bytes + two trailing line pointers
			wantLower: 20, wantUpper: 8191,
			wantTuples: []slotData{{2, "n"}},
		},
		{
			name: "shrunken tuple's leftover bytes are reclaimed",
			page: func(t *testing.T) *Page {
				p := mkPage(t, lp{LPNormal, "aaaa"}, lp{LPNormal, "bbbbbbbb"}, lp{LPNormal, "cc"})
				if err := p.UpdateTuple(1, []byte("BB")); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantGain:  6,
			wantLower: 20, wantUpper: 8184,
			wantTuples: []slotData{{0, "aaaa"}, {1, "BB"}, {2, "cc"}},
		},
		{
			name: "reused slot sits physically below newer tuples",
			page: func(t *testing.T) *Page {
				p := NewPage()
				mustInsert(t, p, "xxxxxx") // slot 0
				mustInsert(t, p, "yyy")    // slot 1
				p.DeleteTuple(0)
				p.Compact()                    // slot 0 UNUSED, "yyy" at 8189
				mustInsert(t, p, "zzzzzzzzzz") // reuses slot 0 at 8179
				mustInsert(t, p, "dd")         // slot 2 at 8177
				p.DeleteTuple(2)
				// slot 0's new home [8182,8192) overlaps slot 1's current bytes [8189,8192)
				return p
			},
			wantGain:  6,
			wantLower: 16, wantUpper: 8179,
			wantTuples: []slotData{{0, "zzzzzzzzzz"}, {1, "yyy"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.page(t)
			if got := p.Compact(); got != tc.wantGain {
				t.Fatalf("Compact() = %d, want %d", got, tc.wantGain)
			}
			if p.Lower() != tc.wantLower || p.Upper() != tc.wantUpper {
				t.Fatalf("lower/upper = %d/%d, want %d/%d", p.Lower(), p.Upper(), tc.wantLower, tc.wantUpper)
			}
			got := collect(p)
			if len(got) != len(tc.wantTuples) {
				t.Fatalf("live tuples = %v, want %v", got, tc.wantTuples)
			}
			for i := range got {
				if got[i] != tc.wantTuples[i] {
					t.Fatalf("live tuples = %v, want %v", got, tc.wantTuples)
				}
			}
			// packed in ascending slot order from the end of the page
			end := PageSize
			for _, st := range tc.wantTuples {
				it, _ := p.ItemID(st.slot)
				if int(it.Off)+int(it.Len) != end {
					t.Fatalf("slot %d ends at %d, want %d (pack ascending slots from the end)", st.slot, int(it.Off)+int(it.Len), end)
				}
				end = int(it.Off)
			}
			for s := 0; s < p.NumSlots(); s++ {
				if it, _ := p.ItemID(SlotID(s)); it.Flags != LPNormal && it != (ItemID{}) {
					t.Fatalf("slot %d = %+v, want all-zero UNUSED", s, it)
				}
			}
			assertHoleZero(t, p)
		})
	}
}

func TestP2_9_Compact_KeepsFlagsAndChecksumField(t *testing.T) {
	p := mkPage(t, lp{LPNormal, "a"}, lp{LPDead, "b"})
	p.buf[4], p.buf[5] = 0x12, 0x34
	p.SetStoredChecksum(0xBEEF)
	p.Compact()
	if p.buf[4] != 0x12 || p.buf[5] != 0x34 || p.StoredChecksum() != 0xBEEF {
		t.Fatalf("header bytes 4..8 = % X, want 12 34 EF BE untouched", p.buf[4:8])
	}
}

// Random workload against a model: slot numbers and bytes of live tuples never change.
func TestP2_9_Compact_RandomModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 9))
	p := NewPage()
	model := map[SlotID]string{}
	for round := 0; round < 300; round++ {
		switch op := rng.IntN(10); {
		case op < 6:
			data := repeat(byte('a'+rng.IntN(26)), 1+rng.IntN(120))
			s, err := p.InsertTuple([]byte(data))
			if errors.Is(err, ErrPageFull) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, taken := model[s]; taken {
				t.Fatalf("round %d: InsertTuple returned live slot %d", round, s)
			}
			model[s] = data
		case op < 9:
			live := slices.Sorted(maps.Keys(model))
			if len(live) == 0 {
				continue
			}
			s := live[rng.IntN(len(live))]
			if err := p.DeleteTuple(s); err != nil {
				t.Fatal(err)
			}
			delete(model, s)
		default:
			oldHole := int(p.Upper()) - int(p.Lower())
			gain := p.Compact()
			if newHole := int(p.Upper()) - int(p.Lower()); newHole-oldHole != gain {
				t.Fatalf("round %d: Compact() = %d, hole grew by %d", round, gain, newHole-oldHole)
			}
			sum := 0
			for _, d := range model {
				sum += len(d)
			}
			if int(p.Upper()) != PageSize-sum {
				t.Fatalf("round %d: upper = %d, want %d", round, p.Upper(), PageSize-sum)
			}
			assertHoleZero(t, p)
		}
		for s, want := range model {
			if got, err := p.GetTuple(s); err != nil || string(got) != want {
				t.Fatalf("round %d: slot %d = %q, %v; want %q", round, s, got, err, want)
			}
		}
	}
}

func TestP2_10_InsertWithFillFactor_Capacity(t *testing.T) {
	tuple := bytes.Repeat([]byte{0xAB}, 100)
	for _, tc := range []struct {
		ff   int
		want int
	}{
		{100, 78},
		{90, 70},
		{50, 39},
		{10, 7},
	} {
		p := NewPage()
		n := 0
		for {
			_, err := p.InsertWithFillFactor(tuple, tc.ff)
			if errors.Is(err, ErrPageFull) {
				break
			}
			if err != nil {
				t.Fatalf("ff=%d: unexpected error %v", tc.ff, err)
			}
			n++
		}
		if n != tc.want {
			t.Errorf("ff=%d: %d tuples fit, want %d", tc.ff, n, tc.want)
		}
	}
}

func TestP2_10_InsertWithFillFactor_Boundary(t *testing.T) {
	// 69 tuples: FreeSpace = 1004. With ff=90 (reserve 819) a 185-byte tuple
	// leaves exactly 819 → allowed; a 186-byte tuple leaves 818 → refused.
	setup := func() *Page {
		p := NewPage()
		for i := 0; i < 69; i++ {
			mustInsert(t, p, repeat('t', 100))
		}
		if p.FreeSpace() != 1004 {
			t.Fatalf("setup FreeSpace = %d, want 1004", p.FreeSpace())
		}
		return p
	}
	p := setup()
	before := snapshot(p)
	if _, err := p.InsertWithFillFactor(make([]byte, 186), 90); !errors.Is(err, ErrPageFull) {
		t.Fatalf("186 bytes: err = %v, want ErrPageFull", err)
	}
	assertUnchanged(t, p, before)
	if s, err := p.InsertWithFillFactor(bytes.Repeat([]byte{1}, 185), 90); err != nil || s != 69 {
		t.Fatalf("185 bytes: slot %d, %v; want slot 69", s, err)
	}
}

func TestP2_10_InsertWithFillFactor_Errors(t *testing.T) {
	tests := []struct {
		name    string
		page    func() *Page
		data    []byte
		ff      int
		wantErr error
		wantOK  bool
	}{
		{"ff below 10", NewPage, []byte("x"), 9, ErrBadFillFactor, false},
		{"ff above 100", NewPage, []byte("x"), 101, ErrBadFillFactor, false},
		{"bad ff checked before empty data", NewPage, nil, 0, ErrBadFillFactor, false},
		{"empty data", NewPage, nil, 90, ErrEmptyTuple, false},
		{"empty page ignores fillfactor", NewPage, make([]byte, 8000), 10, nil, true},
		{"empty page still bounded by space", NewPage, make([]byte, 8181), 100, ErrPageFull, false},
		{"second big tuple refused", func() *Page {
			p := NewPage()
			p.InsertTuple(make([]byte, 8000))
			return p
		}, []byte("x"), 10, ErrPageFull, false},
		{"ff=100 behaves like InsertTuple", func() *Page {
			p := NewPage()
			p.InsertTuple(make([]byte, 8000))
			return p
		}, make([]byte, 172), 100, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.page()
			before := snapshot(p)
			_, err := p.InsertWithFillFactor(tc.data, tc.ff)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !tc.wantOK {
				assertUnchanged(t, p, before)
			}
		})
	}
}
