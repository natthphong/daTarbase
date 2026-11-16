package heap

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// pattern returns n bytes that differ by position, so misordered chunks show up.
func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) + seed
	}
	return b
}

func TestP4_13_Toast_Inline(t *testing.T) {
	tests := []struct {
		name  string
		value []byte
		want  []byte
	}{
		{"short text", []byte("hi"), []byte{0x00, 'h', 'i'}},
		{"empty value", []byte{}, []byte{0x00}},
		{"exactly the threshold stays inline", fill(2000, 'z'), append([]byte{0x00}, fill(2000, 'z')...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tz := NewToaster()
			got, err := tz.Toast(tc.value)
			if err != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("Toast = %d bytes (%v), want %d bytes", len(got), err, len(tc.want))
			}
			if tz.NextValueID != 1 || tz.Rel.Store.NumPages() != 0 || len(tz.Index) != 0 {
				t.Fatalf("inline value must not touch the TOAST relation")
			}
			if len(tc.value) > 0 {
				tc.value[0] ^= 0xFF
				if got[1] == tc.value[0] {
					t.Fatalf("Toast must return a new slice, not alias the input")
				}
			}
		})
	}
}

func TestP4_13_Toast_External(t *testing.T) {
	tz := NewToaster()
	v1 := pattern(5000, 1)
	p1, err := tz.Toast(v1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(p1), "010100000088130000"; got != want {
		t.Fatalf("pointer 1 = %s, want %s", got, want)
	}
	if want := []types.TID{tid(0, 0), tid(0, 1), tid(0, 2)}; !slices.Equal(tz.Index[1], want) {
		t.Fatalf("Index[1] = %v, want %v", tz.Index[1], want)
	}
	// golden layout of the chunk tuples
	page := tz.Rel.Store.Peek(0)
	wantLens := []int{2004, 2004, 1016}
	for seq, l := range wantLens {
		c := page.Slots[seq]
		if len(c) != l {
			t.Fatalf("chunk %d length = %d, want %d", seq, len(c), l)
		}
		if id, s := binary.LittleEndian.Uint32(c[0:4]), binary.LittleEndian.Uint32(c[4:8]); id != 1 || s != uint32(seq) {
			t.Fatalf("chunk %d header = id %d seq %d, want id 1 seq %d", seq, id, s, seq)
		}
		off := seq * ToastChunkSize
		if !bytes.Equal(c[8:], v1[off:off+l-8]) {
			t.Fatalf("chunk %d payload does not match value bytes %d..%d", seq, off, off+l-8)
		}
	}

	p2, err := tz.Toast(pattern(3000, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(p2), "0102000000b80b0000"; got != want {
		t.Fatalf("pointer 2 = %s, want %s", got, want)
	}
	if want := []types.TID{tid(0, 3), tid(0, 4)}; !slices.Equal(tz.Index[2], want) {
		t.Fatalf("Index[2] = %v, want %v", tz.Index[2], want)
	}
	if tz.NextValueID != 3 {
		t.Fatalf("NextValueID = %d, want 3", tz.NextValueID)
	}
}

func TestP4_13_Toast_ChunkBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		wantLens []int // payload length of each chunk
	}{
		{"one byte over the threshold", 2001, []int{1996, 5}},
		{"exactly two chunks", 3992, []int{1996, 1996}},
		{"two chunks plus one byte", 3993, []int{1996, 1996, 1}},
		{"big value spills over pages", 10000, []int{1996, 1996, 1996, 1996, 1996, 20}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tz := NewToaster()
			ptr, err := tz.Toast(fill(tc.size, 'v'))
			if err != nil || len(ptr) != ToastPointerSize || ptr[0] != ToastTagExternal {
				t.Fatalf("Toast(%d) = %x, %v", tc.size, ptr, err)
			}
			tids := tz.Index[1]
			if len(tids) != len(tc.wantLens) {
				t.Fatalf("chunks = %d, want %d", len(tids), len(tc.wantLens))
			}
			for i, ct := range tids {
				c := tz.Rel.Store.Peek(ct.Page).Slots[ct.Slot]
				if len(c)-ToastChunkHeader != tc.wantLens[i] {
					t.Fatalf("chunk %d payload = %d, want %d", i, len(c)-ToastChunkHeader, tc.wantLens[i])
				}
			}
		})
	}
}

func TestP4_14_Detoast_RoundTrip(t *testing.T) {
	tz := NewToaster()
	for _, n := range []int{0, 1, 2000, 2001, 3992, 5000, 10000, 25000} {
		v := pattern(n, byte(n))
		stored, err := tz.Toast(v)
		if err != nil {
			t.Fatalf("Toast(%d): %v", n, err)
		}
		got, err := tz.Detoast(stored)
		if err != nil {
			t.Fatalf("Detoast(Toast(%d bytes)): %v", n, err)
		}
		if got == nil || !bytes.Equal(got, v) {
			t.Fatalf("Detoast(Toast(%d bytes)) = %d bytes, mismatch (nil=%v)", n, len(got), got == nil)
		}
	}
}

func TestP4_14_Detoast_InlineCopies(t *testing.T) {
	tz := NewToaster()
	stored := []byte{0x00, 'a', 'b'}
	got, err := tz.Detoast(stored)
	if err != nil || string(got) != "ab" {
		t.Fatalf("Detoast = %q, %v; want \"ab\"", got, err)
	}
	got[0] = 'X'
	if stored[1] != 'a' {
		t.Fatalf("Detoast must return a copy, not alias the input")
	}
}

func TestP4_14_Detoast_Corrupt(t *testing.T) {
	ptr := func(id, raw uint32) []byte {
		b := []byte{ToastTagExternal, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(b[1:5], id)
		binary.LittleEndian.PutUint32(b[5:9], raw)
		return b
	}
	tests := []struct {
		name   string
		mutate func(tz *Toaster) // applied after toasting a 5000-byte value as id 1
		stored []byte
	}{
		{"empty input", nil, []byte{}},
		{"unknown tag", nil, []byte{0x02, 'x'}},
		{"pointer too short", nil, ptr(1, 5000)[:8]},
		{"pointer too long", nil, append(ptr(1, 5000), 0)},
		{"unknown value id", nil, ptr(9, 5000)},
		{"raw size mismatch", nil, ptr(1, 4999)},
		{"chunk deleted", func(tz *Toaster) { tz.Rel.Delete(tid(0, 1)) }, ptr(1, 5000)},
		{"chunks out of order in the index", func(tz *Toaster) {
			ix := tz.Index[1]
			ix[0], ix[1] = ix[1], ix[0]
		}, ptr(1, 5000)},
		{"chunk missing from the index", func(tz *Toaster) { tz.Index[1] = tz.Index[1][:2] }, ptr(1, 5000)},
		{"index points at another value's chunk", func(tz *Toaster) {
			tz.Toast(fill(3000, 'o')) // id 2 at (0,3),(0,4)
			tz.Index[1][2] = tid(0, 4)
		}, ptr(1, 5000)},
		{"index points at a missing TID", func(tz *Toaster) { tz.Index[1][2] = tid(4, 0) }, ptr(1, 5000)},
		{"chunk shorter than its header", func(tz *Toaster) {
			short, _ := tz.Rel.Insert([]byte{1, 0, 0})
			tz.Index[1][2] = short
		}, ptr(1, 5000)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tz := NewToaster()
			if _, err := tz.Toast(pattern(5000, 3)); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(tz)
			}
			got, err := tz.Detoast(tc.stored)
			if !errors.Is(err, ErrCorruptToast) || got != nil {
				t.Fatalf("Detoast = %d bytes, %v; want nil, ErrCorruptToast", len(got), err)
			}
		})
	}
}
