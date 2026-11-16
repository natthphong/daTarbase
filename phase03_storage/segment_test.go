package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP3_4_SegmentFor(t *testing.T) {
	tests := []struct {
		name     string
		id       types.PageID
		segPages uint32
		wantSeg  uint32
		wantOff  types.PageID
	}{
		{"page 0", 0, 4, 0, 0},
		{"last page of segment 0", 3, 4, 0, 3},
		{"first page of segment 1", 4, 4, 1, 0},
		{"page 9 with 4 per segment", 9, 4, 2, 1},
		{"one page per segment", 7, 1, 7, 0},
		{"postgres: last page of first GB", 131071, PostgresSegPages, 0, 131071},
		{"postgres: first page of 16384.1", 131072, PostgresSegPages, 1, 0},
		{"postgres: 10 GB in", 1310725, PostgresSegPages, 10, 5},
		{"largest valid page", 0xFFFFFFFE, PostgresSegPages, 32767, 131070},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seg, off := SegmentFor(tc.id, tc.segPages)
			if seg != tc.wantSeg || off != tc.wantOff {
				t.Fatalf("SegmentFor(%d, %d) = (%d, %d), want (%d, %d)", tc.id, tc.segPages, seg, off, tc.wantSeg, tc.wantOff)
			}
		})
	}
}

// makeSegments writes files with the given page counts (all but the last
// should be full) and stamps every page with its GLOBAL id.
func makeSegments(t *testing.T, segPages uint32, counts ...int) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "rel")
	global := 0
	for seg, n := range counts {
		var all []byte
		for i := 0; i < n; i++ {
			p := make([]byte, PageSize)
			StampPage(p, types.PageID(global))
			global++
			all = append(all, p...)
		}
		if err := os.WriteFile(SegmentPath(base, uint32(seg)), all, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func TestP3_5_Locate(t *testing.T) {
	base := makeSegments(t, 4, 4, 4, 2) // 10 pages
	d, err := OpenSegmentedDisk(base, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.NumPages() != 10 || d.NumSegments() != 3 {
		t.Fatalf("NumPages, NumSegments = %d, %d; want 10, 3", d.NumPages(), d.NumSegments())
	}
	tests := []struct {
		name      string
		id        types.PageID
		wantSeg   int
		wantLocal types.PageID
		wantErr   error
	}{
		{"page 0", 0, 0, 0, nil},
		{"page 3 end of rel", 3, 0, 3, nil},
		{"page 5 in rel.1", 5, 1, 1, nil},
		{"page 9 last page", 9, 2, 1, nil},
		{"page 10 not allocated in rel.2", 10, 0, 0, ErrPageNotFound},
		{"page 13 rel.3 missing", 13, 0, 0, ErrPageNotFound},
		{"page 400", 400, 0, 0, ErrPageNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seg, local, err := d.Locate(tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if seg != d.segs[tc.wantSeg] || local != tc.wantLocal {
				t.Fatalf("Locate(%d) = (%p, %d), want (segment %d %p, %d)", tc.id, seg, local, tc.wantSeg, d.segs[tc.wantSeg], tc.wantLocal)
			}
		})
	}
}

// ReadPage/WritePage (provided) go through Locate.
func TestP3_5_Locate_ReadWriteThroughSegments(t *testing.T) {
	base := makeSegments(t, 4, 4, 4, 2)
	d, err := OpenSegmentedDisk(base, 4)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, PageSize)
	for id := types.PageID(0); id < 10; id++ {
		if err := d.ReadPage(id, buf); err != nil || PageStamp(buf) != id {
			t.Fatalf("ReadPage(%d): stamp %d, err %v; want stamp %d", id, PageStamp(buf), err, id)
		}
	}
	if err := d.ReadPage(10, buf); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("ReadPage(10) err = %v, want ErrPageNotFound", err)
	}
	StampPage(buf, 777)
	if err := d.WritePage(6, buf); err != nil {
		t.Fatal(err)
	}
	d.Close()
	raw, _ := os.ReadFile(base + ".1")
	if got := PageStamp(raw[2*PageSize:]); got != 777 {
		t.Fatalf("rel.1 local page 2 stamp = %d, want 777", got)
	}
}

func TestP3_6_AllocatePage(t *testing.T) {
	base := filepath.Join(t.TempDir(), "rel")
	d, err := OpenSegmentedDisk(base, 4)
	if err != nil {
		t.Fatal(err)
	}
	size := func(p string) int64 {
		st, err := os.Stat(p)
		if err != nil {
			return -1
		}
		return st.Size()
	}
	for want := types.PageID(0); want < 9; want++ {
		if want == 8 {
			if size(base+".2") != -1 {
				t.Fatalf("rel.2 exists before page 8 is allocated (segments must be created lazily)")
			}
		}
		got, err := d.AllocatePage()
		if err != nil || got != want {
			t.Fatalf("AllocatePage = %d, %v; want %d", got, err, want)
		}
	}
	if d.NumPages() != 9 || d.NumSegments() != 3 {
		t.Fatalf("NumPages, NumSegments = %d, %d; want 9, 3", d.NumPages(), d.NumSegments())
	}
	wantSizes := map[string]int64{base: 32768, base + ".1": 32768, base + ".2": 8192, base + ".3": -1}
	for p, want := range wantSizes {
		if got := size(p); got != want {
			t.Fatalf("size(%s) = %d, want %d (-1 = missing)", filepath.Base(p), got, want)
		}
	}
	// Write via global ids, check the right file got it.
	buf := make([]byte, PageSize)
	StampPage(buf, 8)
	if err := d.WritePage(8, buf); err != nil {
		t.Fatal(err)
	}
	StampPage(buf, 5)
	if err := d.WritePage(5, buf); err != nil {
		t.Fatal(err)
	}
	d.Close()

	raw2, _ := os.ReadFile(base + ".2")
	raw1, _ := os.ReadFile(base + ".1")
	if PageStamp(raw2) != 8 || PageStamp(raw1[PageSize:]) != 5 {
		t.Fatalf("stamps on disk: rel.2[0]=%d rel.1[1]=%d, want 8, 5", PageStamp(raw2), PageStamp(raw1[PageSize:]))
	}

	// Reopen: everything is found again and allocation continues at 9.
	d2, err := OpenSegmentedDisk(base, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.NumPages() != 9 {
		t.Fatalf("reopened NumPages = %d, want 9", d2.NumPages())
	}
	if err := d2.ReadPage(8, buf); err != nil || PageStamp(buf) != 8 {
		t.Fatalf("reopened ReadPage(8) stamp %d err %v, want 8", PageStamp(buf), err)
	}
	if id, err := d2.AllocatePage(); err != nil || id != 9 {
		t.Fatalf("AllocatePage after reopen = %d, %v; want 9", id, err)
	}
}

func TestP3_6_AllocatePage_OneSegmentPerPage(t *testing.T) {
	base := filepath.Join(t.TempDir(), "rel")
	d, err := OpenSegmentedDisk(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for want := types.PageID(0); want < 3; want++ {
		if got, err := d.AllocatePage(); err != nil || got != want {
			t.Fatalf("AllocatePage = %d, %v; want %d", got, err, want)
		}
	}
	if d.NumSegments() != 3 {
		t.Fatalf("NumSegments = %d, want 3", d.NumSegments())
	}
	for _, p := range []string{base, base + ".1", base + ".2"} {
		if st, err := os.Stat(p); err != nil || st.Size() != PageSize {
			t.Fatalf("%s: want one page", filepath.Base(p))
		}
	}
}

func TestP3_6_AllocatePage_CannotCreateSegment(t *testing.T) {
	base := makeSegments(t, 4, 4) // rel is full
	d, err := OpenSegmentedDisk(base, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Something is in the way of rel.1: a directory.
	if err := os.Mkdir(base+".1", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AllocatePage(); err == nil {
		t.Fatal("AllocatePage succeeded, want an error (rel.1 is a directory)")
	}
	if d.NumPages() != 4 || d.NumSegments() != 1 {
		t.Fatalf("after failure NumPages, NumSegments = %d, %d; want 4, 1", d.NumPages(), d.NumSegments())
	}
	os.Remove(base + ".1")
	if id, err := d.AllocatePage(); err != nil || id != 4 {
		t.Fatalf("retry AllocatePage = %d, %v; want 4", id, err)
	}
}
