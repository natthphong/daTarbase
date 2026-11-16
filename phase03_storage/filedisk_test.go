package storage

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

var errBoom = errors.New("boom: injected I/O error")

// fakeFile is an in-memory File that can misbehave.
type fakeFile struct {
	data        []byte
	maxPerWrite int   // >0: WriteAt writes at most this many bytes and returns nil
	writeZero   bool  // WriteAt writes nothing and returns nil
	writeErr    error // WriteAt returns this error (after writing nothing)
	readErr     error // ReadAt returns this error
	readOffs    []int64
	writeOffs   []int64
	writeLens   []int
}

func (f *fakeFile) ReadAt(p []byte, off int64) (int, error) {
	f.readOffs = append(f.readOffs, off)
	if f.readErr != nil {
		return 0, f.readErr
	}
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeFile) WriteAt(p []byte, off int64) (int, error) {
	f.writeOffs = append(f.writeOffs, off)
	f.writeLens = append(f.writeLens, len(p))
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.writeZero {
		return 0, nil
	}
	n := len(p)
	if f.maxPerWrite > 0 && n > f.maxPerWrite {
		n = f.maxPerWrite
	}
	if end := off + int64(n); end > int64(len(f.data)) {
		f.data = append(f.data, make([]byte, end-int64(len(f.data)))...)
	}
	copy(f.data[off:], p[:n])
	return n, nil
}

func (f *fakeFile) Close() error { return nil }

// pageWith returns a PageSize buffer starting with s.
func pageWith(s string) []byte {
	b := make([]byte, PageSize)
	copy(b, s)
	return b
}

// threePageFile builds a real file with pages "page-0","page-1","page-2".
func threePageFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rel")
	var all []byte
	for _, s := range []string{"page-0", "page-1", "page-2"} {
		all = append(all, pageWith(s)...)
	}
	if err := os.WriteFile(path, all, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestP3_1_ReadPage(t *testing.T) {
	path := threePageFile(t)
	d, err := OpenFileDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.NumPages() != 3 {
		t.Fatalf("NumPages = %d, want 3", d.NumPages())
	}
	tests := []struct {
		name    string
		id      types.PageID
		bufLen  int
		want    string
		wantErr error
	}{
		{"first page", 0, PageSize, "page-0", nil},
		{"last page ends exactly at EOF", 2, PageSize, "page-2", nil},
		{"middle page", 1, PageSize, "page-1", nil},
		{"one past the end", 3, PageSize, "", ErrPageNotFound},
		{"far past the end", 1000, PageSize, "", ErrPageNotFound},
		{"buffer too small", 0, PageSize - 1, "", ErrBadBufferSize},
		{"buffer too big", 0, PageSize + 1, "", ErrBadBufferSize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.bufLen)
			err := d.ReadPage(tc.id, buf)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && !bytes.Equal(buf, pageWith(tc.want)) {
				t.Fatalf("page %d starts with %q, want %q (rest zero)", tc.id, buf[:6], tc.want)
			}
		})
	}
}

func TestP3_1_ReadPage_Offsets(t *testing.T) {
	ff := &fakeFile{data: make([]byte, 3*PageSize)}
	copy(ff.data[2*PageSize:], "page-2")
	d := NewFileDisk(ff, 3)
	buf := make([]byte, PageSize)
	if err := d.ReadPage(2, buf); err != nil {
		t.Fatal(err)
	}
	if len(ff.readOffs) != 1 || ff.readOffs[0] != 16384 {
		t.Fatalf("ReadAt offsets = %v, want [16384]", ff.readOffs)
	}
	if string(buf[:6]) != "page-2" {
		t.Fatalf("buf = %q, want page-2", buf[:6])
	}
	// No I/O for pages that don't exist.
	if err := d.ReadPage(3, buf); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("err = %v, want ErrPageNotFound", err)
	}
	if len(ff.readOffs) != 1 {
		t.Fatalf("ReadAt called %d times, want 1 (no I/O past the end)", len(ff.readOffs))
	}
}

// Page 600000 starts at 4915200000, beyond 2^32: the offset must be int64 math.
func TestP3_1_ReadPage_LargeOffsetNoOverflow(t *testing.T) {
	ff := &fakeFile{readErr: errBoom}
	d := NewFileDisk(ff, 700000)
	err := d.ReadPage(600000, make([]byte, PageSize))
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want wrapped errBoom", err)
	}
	if len(ff.readOffs) != 1 || ff.readOffs[0] != 4915200000 {
		t.Fatalf("ReadAt offsets = %v, want [4915200000]", ff.readOffs)
	}
}

func TestP3_1_ReadPage_TruncatedFile(t *testing.T) {
	path := threePageFile(t)
	d, err := OpenFileDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Someone cuts the file in the middle of page 1.
	if err := os.Truncate(path, PageSize+100); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, PageSize)
	if err := d.ReadPage(0, buf); err != nil {
		t.Fatalf("page 0: err = %v, want nil", err)
	}
	if err := d.ReadPage(1, buf); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("page 1 (half there): err = %v, want ErrPageNotFound", err)
	}
	if err := d.ReadPage(2, buf); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("page 2 (gone): err = %v, want ErrPageNotFound", err)
	}
}

func TestP3_2_WritePage(t *testing.T) {
	path := threePageFile(t)
	d, err := OpenFileDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.WritePage(1, pageWith("NEW-1")); err != nil {
		t.Fatalf("WritePage(1): %v", err)
	}
	buf := make([]byte, PageSize)
	if err := d.ReadPage(1, buf); err != nil || string(buf[:5]) != "NEW-1" {
		t.Fatalf("read back = %q, %v; want NEW-1", buf[:5], err)
	}
	d.Close()

	raw, _ := os.ReadFile(path)
	if len(raw) != 3*PageSize {
		t.Fatalf("file size = %d, want %d", len(raw), 3*PageSize)
	}
	if string(raw[PageSize:PageSize+5]) != "NEW-1" || string(raw[0:6]) != "page-0" || string(raw[2*PageSize:2*PageSize+6]) != "page-2" {
		t.Fatalf("file bytes wrong: p0=%q p1=%q p2=%q", raw[0:6], raw[PageSize:PageSize+5], raw[2*PageSize:2*PageSize+6])
	}
}

func TestP3_2_WritePage_Errors(t *testing.T) {
	tests := []struct {
		name    string
		id      types.PageID
		bufLen  int
		wantErr error
	}{
		{"page not allocated", 3, PageSize, ErrPageNotFound},
		{"buffer too small", 0, 100, ErrBadBufferSize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ff := &fakeFile{data: make([]byte, 3*PageSize)}
			d := NewFileDisk(ff, 3)
			if err := d.WritePage(tc.id, make([]byte, tc.bufLen)); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(ff.writeOffs) != 0 {
				t.Fatalf("WriteAt called %d times, want 0", len(ff.writeOffs))
			}
		})
	}
}

func TestP3_2_WritePage_ShortWritesAreRetried(t *testing.T) {
	ff := &fakeFile{data: make([]byte, 3*PageSize), maxPerWrite: 3000}
	d := NewFileDisk(ff, 3)
	page := make([]byte, PageSize)
	for i := range page {
		page[i] = byte(i % 251)
	}
	if err := d.WritePage(1, page); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	wantOffs := []int64{8192, 11192, 14192}
	wantLens := []int{8192, 5192, 2192}
	if len(ff.writeOffs) != 3 {
		t.Fatalf("WriteAt calls = %d (offs %v), want 3", len(ff.writeOffs), ff.writeOffs)
	}
	for i := range wantOffs {
		if ff.writeOffs[i] != wantOffs[i] || ff.writeLens[i] != wantLens[i] {
			t.Fatalf("call %d: WriteAt(off=%d, %d bytes), want (off=%d, %d bytes)", i, ff.writeOffs[i], ff.writeLens[i], wantOffs[i], wantLens[i])
		}
	}
	if !bytes.Equal(ff.data[PageSize:2*PageSize], page) {
		t.Fatal("page 1 bytes on disk differ from what was written")
	}
}

func TestP3_2_WritePage_NoProgressAndErrors(t *testing.T) {
	t.Run("zero bytes and nil error", func(t *testing.T) {
		ff := &fakeFile{data: make([]byte, PageSize), writeZero: true}
		err := NewFileDisk(ff, 1).WritePage(0, make([]byte, PageSize))
		if !errors.Is(err, ErrShortWrite) {
			t.Fatalf("err = %v, want ErrShortWrite", err)
		}
		if len(ff.writeOffs) != 1 {
			t.Fatalf("WriteAt calls = %d, want 1 (stop when no progress)", len(ff.writeOffs))
		}
	})
	t.Run("error from WriteAt", func(t *testing.T) {
		ff := &fakeFile{data: make([]byte, PageSize), writeErr: errBoom}
		err := NewFileDisk(ff, 1).WritePage(0, make([]byte, PageSize))
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want wrapped errBoom", err)
		}
	})
}

func TestP3_3_AllocatePage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rel")
	d, err := OpenFileDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	for want := types.PageID(0); want < 3; want++ {
		got, err := d.AllocatePage()
		if err != nil || got != want {
			t.Fatalf("AllocatePage = %d, %v; want %d", got, err, want)
		}
		st, _ := os.Stat(path)
		if st.Size() != int64(want+1)*PageSize {
			t.Fatalf("after page %d: file size %d, want %d", want, st.Size(), int64(want+1)*PageSize)
		}
	}
	if d.NumPages() != 3 {
		t.Fatalf("NumPages = %d, want 3", d.NumPages())
	}
	buf := pageWith("garbage")
	if err := d.ReadPage(2, buf); err != nil || !bytes.Equal(buf, make([]byte, PageSize)) {
		t.Fatalf("new page 2 not all zero (err %v)", err)
	}
	if err := d.WritePage(1, pageWith("hello")); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d2, err := OpenFileDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.NumPages() != 3 {
		t.Fatalf("reopened NumPages = %d, want 3", d2.NumPages())
	}
	if err := d2.ReadPage(1, buf); err != nil || string(buf[:5]) != "hello" {
		t.Fatalf("reopened page 1 = %q, %v; want hello", buf[:5], err)
	}
}

func TestP3_3_AllocatePage_ShortWritesAndOffsets(t *testing.T) {
	ff := &fakeFile{data: make([]byte, 2*PageSize), maxPerWrite: 5000}
	for i := range ff.data {
		ff.data[i] = 0xEE
	}
	d := NewFileDisk(ff, 2)
	id, err := d.AllocatePage()
	if err != nil || id != 2 {
		t.Fatalf("AllocatePage = %d, %v; want 2", id, err)
	}
	if len(ff.writeOffs) != 2 || ff.writeOffs[0] != 16384 || ff.writeOffs[1] != 21384 {
		t.Fatalf("WriteAt offsets = %v, want [16384 21384]", ff.writeOffs)
	}
	if len(ff.data) != 3*PageSize || !bytes.Equal(ff.data[2*PageSize:], make([]byte, PageSize)) {
		t.Fatalf("new page is not %d zero bytes at the end", PageSize)
	}
}

func TestP3_3_AllocatePage_Errors(t *testing.T) {
	t.Run("write fails: NumPages unchanged", func(t *testing.T) {
		ff := &fakeFile{data: make([]byte, 2*PageSize), writeErr: errBoom}
		d := NewFileDisk(ff, 2)
		if _, err := d.AllocatePage(); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		if d.NumPages() != 2 {
			t.Fatalf("NumPages = %d, want 2", d.NumPages())
		}
		ff.writeErr = nil
		if id, err := d.AllocatePage(); err != nil || id != 2 {
			t.Fatalf("retry AllocatePage = %d, %v; want 2", id, err)
		}
	})
	t.Run("no progress: ErrShortWrite", func(t *testing.T) {
		d := NewFileDisk(&fakeFile{writeZero: true}, 0)
		if _, err := d.AllocatePage(); !errors.Is(err, ErrShortWrite) {
			t.Fatalf("err = %v, want ErrShortWrite", err)
		}
		if d.NumPages() != 0 {
			t.Fatalf("NumPages = %d, want 0", d.NumPages())
		}
	})
	t.Run("InvalidPageID is reserved", func(t *testing.T) {
		ff := &fakeFile{}
		d := NewFileDisk(ff, math.MaxUint32)
		if _, err := d.AllocatePage(); !errors.Is(err, ErrTooManyPages) {
			t.Fatalf("err = %v, want ErrTooManyPages", err)
		}
		if len(ff.writeOffs) != 0 || d.NumPages() != math.MaxUint32 {
			t.Fatalf("writes = %d, NumPages = %d; want no I/O, unchanged", len(ff.writeOffs), d.NumPages())
		}
	})
}
