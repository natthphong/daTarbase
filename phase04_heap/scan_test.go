package heap

import (
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP4_9_SeqScan(t *testing.T) {
	tests := []struct {
		name  string
		store func() *PageStore
		want  string
		reads int
	}{
		{
			name: "doc example: dead slot and empty page",
			store: func() *PageStore {
				s := mkStore([]string{"a", "b", "c"}, nil, []string{"d"})
				kill(s, tid(0, 1))
				return s
			},
			want:  "(0,0)=a (0,2)=c (2,0)=d",
			reads: 3,
		},
		{
			name:  "no pages",
			store: func() *PageStore { return NewPageStore() },
			want:  "",
			reads: 0,
		},
		{
			name: "page with only dead tuples is still read",
			store: func() *PageStore {
				s := mkStore([]string{"x", "y"}, []string{"z"})
				kill(s, tid(0, 0))
				kill(s, tid(0, 1))
				return s
			},
			want:  "(1,0)=z",
			reads: 2,
		},
		{
			name: "updated versions are dead too",
			store: func() *PageStore {
				s := mkStore([]string{"v1", "v2"})
				forward(s, tid(0, 0), tid(0, 1))
				return s
			},
			want:  "(0,1)=v2",
			reads: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.store()
			sc := NewSeqScan(s)
			assertTuples(t, drain(t, sc.Next), tc.want)
			if s.Reads != tc.reads {
				t.Fatalf("Reads = %d, want %d", s.Reads, tc.reads)
			}
			for i := 0; i < 3; i++ {
				if _, ok, err := sc.Next(); ok || err != nil {
					t.Fatalf("Next after end = ok %v err %v, want false nil", ok, err)
				}
			}
			if s.Reads != tc.reads {
				t.Fatalf("Next after end read pages: Reads = %d, want %d", s.Reads, tc.reads)
			}
		})
	}
}

func TestP4_9_SeqScan_Lazy(t *testing.T) {
	s := mkStore([]string{"a", "b"}, []string{"c"}, []string{"d"})
	sc := NewSeqScan(s)
	if s.Reads != 0 {
		t.Fatalf("NewSeqScan must not read: Reads = %d", s.Reads)
	}
	wantReads := []int{1, 1, 2, 3}
	for i, w := range wantReads {
		if _, ok, err := sc.Next(); !ok || err != nil {
			t.Fatalf("Next #%d: ok=%v err=%v", i, ok, err)
		}
		if s.Reads != w {
			t.Fatalf("after Next #%d: Reads = %d, want %d", i, s.Reads, w)
		}
	}
}

func TestP4_9_SeqScan_SnapshotOfLength(t *testing.T) {
	s := mkStore([]string{"a"}, []string{"b"})
	sc := NewSeqScan(s)
	_, p := s.Extend()
	p.Add([]byte("late"))
	assertTuples(t, drain(t, sc.Next), "(0,0)=a (1,0)=b")
	if s.Reads != 2 {
		t.Fatalf("Reads = %d, want 2 (page 2 is not part of the scan)", s.Reads)
	}
}

func TestP4_10_SyncScan(t *testing.T) {
	s := mkStore([]string{"p0"}, []string{"p1"}, []string{"p2"}, []string{"p3"})
	loc := &SyncLocation{Page: 2}
	sc := NewSyncScan(s, loc)
	wantData := []string{"p2", "p3", "p0", "p1"}
	wantTID := []types.TID{tid(2, 0), tid(3, 0), tid(0, 0), tid(1, 0)}
	wantLoc := []types.PageID{2, 3, 0, 1}
	for i := range wantData {
		tu, ok, err := sc.Next()
		if !ok || err != nil {
			t.Fatalf("Next #%d: ok=%v err=%v", i, ok, err)
		}
		if string(tu.Data) != wantData[i] || tu.TID != wantTID[i] {
			t.Fatalf("Next #%d = %v %q, want %v %q", i, tu.TID, tu.Data, wantTID[i], wantData[i])
		}
		if loc.Page != wantLoc[i] {
			t.Fatalf("after Next #%d loc.Page = %d, want %d", i, loc.Page, wantLoc[i])
		}
	}
	for i := 0; i < 2; i++ {
		if _, ok, err := sc.Next(); ok || err != nil {
			t.Fatalf("Next after end = %v %v, want false nil", ok, err)
		}
	}
	if s.Reads != 4 {
		t.Fatalf("Reads = %d, want 4", s.Reads)
	}
}

func TestP4_10_SyncScan_Cases(t *testing.T) {
	build := func() *PageStore {
		s := mkStore([]string{"a", "b"}, nil, []string{"c", "d", "e"}, []string{"f"})
		kill(s, tid(2, 1))
		return s
	}
	tests := []struct {
		name    string
		loc     *SyncLocation
		want    string
		wantLoc types.PageID
	}{
		{"nil location starts at 0", nil, "(0,0)=a (0,1)=b (2,0)=c (2,2)=e (3,0)=f", 0},
		{"start 0", &SyncLocation{Page: 0}, "(0,0)=a (0,1)=b (2,0)=c (2,2)=e (3,0)=f", 3},
		{"start on the empty page", &SyncLocation{Page: 1}, "(2,0)=c (2,2)=e (3,0)=f (0,0)=a (0,1)=b", 0},
		{"start at last page", &SyncLocation{Page: 3}, "(3,0)=f (0,0)=a (0,1)=b (2,0)=c (2,2)=e", 2},
		{"start beyond the end falls back to 0", &SyncLocation{Page: 17}, "(0,0)=a (0,1)=b (2,0)=c (2,2)=e (3,0)=f", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := build()
			sc := NewSyncScan(s, tc.loc)
			assertTuples(t, drain(t, sc.Next), tc.want)
			if s.Reads != 4 {
				t.Fatalf("Reads = %d, want 4 (every page once)", s.Reads)
			}
			if tc.loc != nil && tc.loc.Page != tc.wantLoc {
				t.Fatalf("final loc.Page = %d, want %d", tc.loc.Page, tc.wantLoc)
			}
		})
	}
}

func TestP4_10_SyncScan_SecondScanJoinsFirst(t *testing.T) {
	s := mkStore([]string{"a"}, []string{"b"}, []string{"c"}, []string{"d"}, []string{"e"})
	loc := &SyncLocation{}
	first := NewSyncScan(s, loc)
	for i := 0; i < 3; i++ { // first scan reads pages 0, 1, 2
		first.Next()
	}
	if loc.Page != 2 {
		t.Fatalf("loc.Page = %d, want 2", loc.Page)
	}
	second := NewSyncScan(s, loc)
	tu, _, _ := second.Next()
	if tu.TID != tid(2, 0) {
		t.Fatalf("second scan started at %v, want (2,0)", tu.TID)
	}
	rest := drain(t, second.Next)
	assertTuples(t, append([]Tuple{tu}, rest...), "(2,0)=c (3,0)=d (4,0)=e (0,0)=a (1,0)=b")
	assertTuples(t, drain(t, first.Next), "(3,0)=d (4,0)=e")
}

func TestP4_10_SyncScan_Empty(t *testing.T) {
	s := NewPageStore()
	loc := &SyncLocation{Page: 3}
	if _, ok, err := NewSyncScan(s, loc).Next(); ok || err != nil {
		t.Fatalf("empty relation: ok=%v err=%v", ok, err)
	}
	if s.Reads != 0 || loc.Page != 3 {
		t.Fatalf("Reads=%d loc=%d, want 0 reads and untouched loc", s.Reads, loc.Page)
	}
}

// rangeFixture: page 0: 3 tuples, page 1: 3 tuples ((1,1) dead), page 2: 2 tuples.
func rangeFixture() *PageStore {
	s := mkStore([]string{"a0", "a1", "a2"}, []string{"b0", "b1", "b2"}, []string{"c0", "c1"})
	kill(s, tid(1, 1))
	return s
}

func TestP4_11_TIDRangeScan(t *testing.T) {
	tests := []struct {
		name   string
		lo, hi types.TID
		want   string
		reads  int
	}{
		{"spanning three pages", tid(0, 2), tid(2, 0), "(0,2)=a2 (1,0)=b0 (1,2)=b2 (2,0)=c0", 3},
		{"slots past the end of a page", tid(1, 5), tid(1, 9), "", 1},
		{"hi beyond the file is clamped", tid(2, 1), tid(9, 0), "(2,1)=c1", 1},
		{"hi < lo reads nothing", tid(2, 0), tid(1, 0), "", 0},
		{"single TID", tid(0, 1), tid(0, 1), "(0,1)=a1", 1},
		{"single dead TID", tid(1, 1), tid(1, 1), "", 1},
		{"whole table with InvalidPageID", tid(0, 0), types.TID{Page: types.InvalidPageID, Slot: 65535},
			"(0,0)=a0 (0,1)=a1 (0,2)=a2 (1,0)=b0 (1,2)=b2 (2,0)=c0 (2,1)=c1", 3},
		{"lo beyond the file", tid(3, 0), tid(5, 0), "", 0},
		{"same page, lo slot > 0", tid(0, 1), tid(0, 65535), "(0,1)=a1 (0,2)=a2", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := rangeFixture()
			got := TIDRangeScan(s, tc.lo, tc.hi)
			if got == nil {
				t.Fatalf("result must be non-nil")
			}
			assertTuples(t, got, tc.want)
			if s.Reads != tc.reads {
				t.Fatalf("Reads = %d, want %d", s.Reads, tc.reads)
			}
		})
	}
}

func TestP4_11_TIDRangeScan_EmptyStore(t *testing.T) {
	s := NewPageStore()
	got := TIDRangeScan(s, tid(0, 0), types.TID{Page: types.InvalidPageID, Slot: 65535})
	if got == nil || len(got) != 0 || s.Reads != 0 {
		t.Fatalf("empty store: got %v (nil=%v), %d reads", got, got == nil, s.Reads)
	}
}

func TestP4_12_BitmapHeapScan(t *testing.T) {
	build := func() *PageStore {
		return mkStore(
			[]string{"a0", "a1", "a2"},
			[]string{"b0"},
			[]string{"c0", "c1", "c2"},
			[]string{"d0", "d1"},
		)
	}
	tests := []struct {
		name  string
		tids  []types.TID
		lossy []types.PageID
		prep  func(*PageStore)
		want  string
		reads int
	}{
		{
			name:  "doc example",
			tids:  []types.TID{tid(3, 1), tid(0, 2), tid(3, 1), tid(0, 0), tid(7, 0)},
			lossy: []types.PageID{2},
			want:  "(0,0)=a0 (0,2)=a2 (2,0)=c0 (2,1)=c1 (2,2)=c2 (3,1)=d1",
			reads: 3,
		},
		{
			name:  "empty input",
			want:  "",
			reads: 0,
		},
		{
			name:  "random order, same page many times, read once",
			tids:  []types.TID{tid(2, 2), tid(0, 1), tid(2, 0), tid(0, 1), tid(2, 2), tid(2, 0)},
			want:  "(0,1)=a1 (2,0)=c0 (2,2)=c2",
			reads: 2,
		},
		{
			name:  "stale index entries: dead tuple and missing slot are skipped",
			tids:  []types.TID{tid(1, 0), tid(1, 4), tid(0, 0)},
			prep:  func(s *PageStore) { kill(s, tid(1, 0)) },
			want:  "(0,0)=a0",
			reads: 2,
		},
		{
			name:  "lossy page with exact TIDs too: lossy wins, no duplicates",
			tids:  []types.TID{tid(2, 1), tid(2, 1)},
			lossy: []types.PageID{2, 2},
			prep:  func(s *PageStore) { kill(s, tid(2, 0)) },
			want:  "(2,1)=c1 (2,2)=c2",
			reads: 1,
		},
		{
			name:  "only lossy pages, unsorted",
			lossy: []types.PageID{3, 1, 9},
			want:  "(1,0)=b0 (3,0)=d0 (3,1)=d1",
			reads: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := build()
			if tc.prep != nil {
				tc.prep(s)
			}
			got := BitmapHeapScan(s, tc.tids, tc.lossy)
			if got == nil {
				t.Fatalf("result must be non-nil")
			}
			assertTuples(t, got, tc.want)
			if s.Reads != tc.reads {
				t.Fatalf("Reads = %d, want %d (each distinct existing page once)", s.Reads, tc.reads)
			}
		})
	}
}

func TestP4_12_BitmapHeapScan_ManyTIDs(t *testing.T) {
	// 20 pages × 10 tuples; the "index" returns every tuple of the even pages,
	// in reverse order — a plain index scan would read 100 times.
	var pages [][]string
	for p := 0; p < 20; p++ {
		var row []string
		for s := 0; s < 10; s++ {
			row = append(row, "t")
		}
		pages = append(pages, row)
	}
	store := mkStore(pages...)
	var tids []types.TID
	for p := 18; p >= 0; p -= 2 {
		for s := 9; s >= 0; s-- {
			tids = append(tids, tid(p, s))
		}
	}
	got := BitmapHeapScan(store, tids, nil)
	if len(got) != 100 || store.Reads != 10 {
		t.Fatalf("got %d tuples with %d reads, want 100 tuples with 10 reads", len(got), store.Reads)
	}
	for i := 1; i < len(got); i++ {
		if !got[i-1].TID.Less(got[i].TID) {
			t.Fatalf("output not in TID order at %d: %v then %v", i, got[i-1].TID, got[i].TID)
		}
	}
}

func TestP4_12_BitmapHeapScan_PagesPastEnd(t *testing.T) {
	s := mkStore([]string{"a"})
	got := BitmapHeapScan(s,
		[]types.TID{tid(5, 0), {Page: types.InvalidPageID, Slot: 0}, tid(1, 0)},
		[]types.PageID{3, types.InvalidPageID})
	if got == nil || len(got) != 0 {
		t.Fatalf("got %s, want no tuples", fmtTuples(got))
	}
	if s.Reads != 0 {
		t.Fatalf("Reads = %d, want 0: pages past the end must not be read", s.Reads)
	}
}
