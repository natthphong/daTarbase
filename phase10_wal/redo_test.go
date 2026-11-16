package wal

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func updRec(lsn types.LSN, page types.PageID, off int, before, after string) Record {
	return Record{LSN: lsn, Txn: 1, Type: RecUpdate,
		Body: UpdateBody{Page: page, Off: uint16(off), Before: []byte(before), After: []byte(after)}.Encode()}
}

func clrRec(lsn types.LSN, page types.PageID, off int, data string) Record {
	return Record{LSN: lsn, Txn: 1, Type: RecCLR,
		Body: CLRBody{Page: page, Off: uint16(off), Data: []byte(data), UndoNext: 8}.Encode()}
}

func fpiRec(lsn types.LSN, page types.PageID, img [PageDataSize]byte) Record {
	return Record{LSN: lsn, Type: RecFPI, Body: FPIBody{Page: page, Image: img}.Encode()}
}

func TestP10_9_RedoRecord_Sequence(t *testing.T) {
	p := &Page{LSN: 100}
	img := pageData(0, "FPI image")
	steps := []struct {
		name        string
		rec         Record
		wantApplied bool
		wantLSN     types.LSN
		wantData    [PageDataSize]byte
	}{
		{"newer update applies", updRec(150, 0, 2, "\x00\x00", "ab"), true, 150, pageData(2, "ab")},
		{"same LSN again is skipped", updRec(150, 0, 2, "\x00\x00", "ab"), false, 150, pageData(2, "ab")},
		{"older update is skipped", updRec(120, 0, 0, "\x00\x00", "zz"), false, 150, pageData(2, "ab")},
		{"CLR applies its data", clrRec(160, 0, 2, "\x00\x00"), true, 160, [PageDataSize]byte{}},
		{"older FPI still restores", fpiRec(90, 0, img), true, 90, img},
		{"update after the FPI applies again", updRec(150, 0, 0, "FP", "fp"), true, 150, pageData(0, "fpI image")},
		{"begin record: no-op", Record{LSN: 900, Txn: 1, Type: RecBegin}, false, 150, pageData(0, "fpI image")},
		{"commit record: no-op", Record{LSN: 901, Txn: 1, Type: RecCommit}, false, 150, pageData(0, "fpI image")},
		{"checkpoint record: no-op", Record{LSN: 902, Type: RecCheckpoint, Body: CheckpointBody{}.Encode()}, false, 150, pageData(0, "fpI image")},
	}
	for _, s := range steps {
		applied, err := RedoRecord(p, s.rec)
		if err != nil {
			t.Fatalf("%s: err = %v", s.name, err)
		}
		if applied != s.wantApplied || p.LSN != s.wantLSN || p.Data != s.wantData {
			t.Fatalf("%s: applied=%v LSN=%d data=%q, want %v %d %q",
				s.name, applied, p.LSN, p.Data[:10], s.wantApplied, s.wantLSN, s.wantData[:10])
		}
	}
}

func TestP10_9_RedoRecord_Idempotent(t *testing.T) {
	recs := []Record{
		fpiRec(37, 7, pageData(0, "base")),
		updRec(134, 7, 4, "\x00\x00", "XY"),
		updRec(175, 7, 0, "ba", "BA"),
		clrRec(216, 7, 0, "ba"),
	}
	once := &Page{}
	for _, r := range recs {
		RedoRecord(once, r)
	}
	twice := &Page{}
	for range 2 {
		for _, r := range recs {
			RedoRecord(twice, r)
		}
	}
	want := Page{LSN: 216, Data: pageData(0, "baseXY")}
	if *once != want || *twice != want {
		t.Fatalf("once = %d %q, twice = %d %q; want %d %q",
			once.LSN, once.Data[:8], twice.LSN, twice.Data[:8], want.LSN, want.Data[:8])
	}
}

func TestP10_9_RedoRecord_TornPage(t *testing.T) {
	// A torn page: header (LSN 175) and first half from the new version,
	// second half from an old one. Without the FPI redo would skip everything.
	torn := Page{LSN: 175}
	copy(torn.Data[:], "NEWNEW")
	copy(torn.Data[40:], "OLD")
	recs := []Record{
		fpiRec(37, 7, pageData(40, "new")),
		updRec(134, 7, 0, "\x00\x00\x00", "NEW"),
		updRec(175, 7, 3, "\x00\x00\x00", "NEW"),
	}
	for _, r := range recs {
		if _, err := RedoRecord(&torn, r); err != nil {
			t.Fatal(err)
		}
	}
	want := pageData(0, "NEWNEW")
	copy(want[40:], "new")
	if torn.Data != want || torn.LSN != 175 {
		t.Fatalf("repaired page = %d %q, want 175 %q", torn.LSN, torn.Data[:], want[:])
	}
}

func TestP10_9_RedoRecord_Corrupt(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
	}{
		{"short update body", Record{LSN: 50, Type: RecUpdate, Body: []byte{1, 2, 3}}},
		{"update past page end", updRec(50, 0, 63, "\x00\x00", "xy")},
		{"CLR past page end", clrRec(50, 0, 64, "x")},
		{"FPI body wrong size", Record{LSN: 50, Type: RecFPI, Body: make([]byte, 10)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Page{LSN: 10}
			applied, err := RedoRecord(p, tc.rec)
			if !errors.Is(err, ErrCorruptRecord) || applied {
				t.Fatalf("got %v, %v; want false, ErrCorruptRecord", applied, err)
			}
			if *p != (Page{LSN: 10}) {
				t.Fatalf("page changed on error")
			}
		})
	}
}

func TestP10_12_RedoPass_Scenario(t *testing.T) {
	crashed := scenario(t).Crash(0)
	db := Open(crashed)
	dpt := map[types.PageID]types.LSN{1: 37, 2: 204, 5: 518}
	n, err := db.RedoPass(dpt, 37)
	if err != nil || n != 8 {
		t.Fatalf("RedoPass = %d, %v; want 8 applied", n, err)
	}
	check := func(when string) {
		t.Helper()
		want := map[types.PageID]Page{
			1: {LSN: 477, Data: pageData(0, "t1t3")},
			2: {LSN: 656, Data: pageData(0, "t2xx")},
			5: {LSN: 615, Data: pageData(0, "t3")},
		}
		for id, w := range want {
			if got := *db.BP.Page(id); got != w {
				t.Fatalf("%s: page %d = LSN %d %q, want LSN %d %q", when, id, got.LSN, got.Data[:4], w.LSN, w.Data[:4])
			}
		}
		for id, w := range dpt {
			if rec, dirty := db.BP.RecLSN(id); !dirty || rec != w {
				t.Fatalf("%s: page %d recLSN=%d dirty=%v, want dirty with %d", when, id, rec, dirty, w)
			}
		}
		if _, dirty := db.BP.RecLSN(6); dirty {
			t.Fatalf("page 6 (its records were lost) must not be dirty")
		}
	}
	check("first run")
	n, err = db.RedoPass(dpt, 37)
	if err != nil || n != 8 {
		t.Fatalf("second RedoPass = %d, %v; want 8", n, err)
	}
	check("second run")
	if db.Log.End() != 726 || db.Disk.PageWrites() != 0 {
		t.Fatalf("redo must not log or write pages (End %d, writes %d)", db.Log.End(), db.Disk.PageWrites())
	}
}

func TestP10_12_RedoPass_DPTFilter(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()
	db.Update(tx, 1, 0, []byte("aa")) // FPI p1@37, UPD@134
	db.BP.FlushPage(1)                // disk page 1: "aa", LSN 134
	db.Update(tx, 1, 2, []byte("bb")) // FPI p1@175 (image "aa"), UPD@272
	db.Update(tx, 2, 0, []byte("cc")) // FPI p2@313, UPD@410
	db.Log.FlushUpTo(db.Log.End())    // End 451

	tests := []struct {
		name      string
		dpt       map[types.PageID]types.LSN
		from      types.LSN
		wantN     int
		wantPage1 Page
		wantPage2 Page
	}{
		{
			name:      "page 1 recLSN 175: the older FPI@37 must not roll it back",
			dpt:       map[types.PageID]types.LSN{1: 175, 2: 313},
			from:      37,
			wantN:     4, // FPI@175 UPD@272 FPI@313 UPD@410
			wantPage1: Page{LSN: 272, Data: pageData(0, "aabb")},
			wantPage2: Page{LSN: 410, Data: pageData(0, "cc")},
		},
		{
			name:      "page 2 not in DPT is never touched",
			dpt:       map[types.PageID]types.LSN{1: 175},
			from:      175,
			wantN:     2,
			wantPage1: Page{LSN: 272, Data: pageData(0, "aabb")},
			wantPage2: Page{},
		},
		{
			name:      "empty DPT: nothing to do",
			dpt:       map[types.PageID]types.LSN{},
			from:      451,
			wantN:     0,
			wantPage1: Page{LSN: 134, Data: pageData(0, "aa")},
			wantPage2: Page{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db2 := Open(db.Disk.Clone())
			n, err := db2.RedoPass(tc.dpt, tc.from)
			if err != nil || n != tc.wantN {
				t.Fatalf("RedoPass = %d, %v; want %d", n, err, tc.wantN)
			}
			if got := *db2.BP.Page(1); got != tc.wantPage1 {
				t.Fatalf("page 1 = %d %q, want %d %q", got.LSN, got.Data[:4], tc.wantPage1.LSN, tc.wantPage1.Data[:4])
			}
			if got := *db2.BP.Page(2); got != tc.wantPage2 {
				t.Fatalf("page 2 = %d %q, want %d %q", got.LSN, got.Data[:4], tc.wantPage2.LSN, tc.wantPage2.Data[:4])
			}
		})
	}
	if _, err := Open(db.Disk.Clone()).RedoPass(nil, 3); !errors.Is(err, ErrInvalidLSN) {
		t.Fatalf("RedoPass from 3: err = %v, want ErrInvalidLSN", err)
	}
}

// Redo repeats history: a loser's update and the CLR that compensated it are
// both replayed, and the page ends in the post-CLR state.
func TestP10_12_RedoPass_RepeatsHistoryWithCLRs(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()                                                 // @8
	db.Update(tx, 1, 0, []byte("aa"))                                // FPI@37, UPD@134
	db.Update(tx, 2, 0, []byte("zz"))                                // FPI@175, UPD@272
	clr := db.Log.Append(Record{Type: RecCLR, Txn: tx, PrevLSN: 272, // @313
		Body: CLRBody{Page: 1, Off: 0, Data: []byte("\x00\x00"), UndoNext: 8}.Encode()})
	db.Log.FlushUpTo(clr)
	db2 := Open(db.Crash(0))
	n, err := db2.RedoPass(map[types.PageID]types.LSN{1: 37, 2: 175}, 37)
	if err != nil || n != 5 {
		t.Fatalf("RedoPass = %d, %v; want 5 (FPI, UPD, FPI, UPD, CLR)", n, err)
	}
	if p := *db2.BP.Page(1); p != (Page{LSN: 313}) {
		t.Fatalf("page 1 = LSN %d %q, want LSN 313 all zero", p.LSN, p.Data[:2])
	}
	if p := *db2.BP.Page(2); p != (Page{LSN: 272, Data: pageData(0, "zz")}) {
		t.Fatalf("page 2 = LSN %d %q, want LSN 272 \"zz\"", p.LSN, p.Data[:2])
	}
	if got := db2.BP.DirtyPages(); len(got) != 2 {
		t.Fatalf("dirty pages = %v, want [1 2]", got)
	}
}
