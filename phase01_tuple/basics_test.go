package tuple

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP1_1_AlignUp(t *testing.T) {
	tests := []struct {
		name             string
		off, align, want int
	}{
		{"zero stays zero", 0, 8, 0},
		{"header 13 rounds to 16 (hoff)", 13, 8, 16},
		{"already aligned is unchanged", 16, 8, 16},
		{"float8 after bool at 17", 17, 8, 24},
		{"int4 after bool at 21", 21, 4, 24},
		{"one past aligned int4", 25, 4, 28},
		{"short alignment", 5, 2, 6},
		{"short already aligned", 6, 2, 6},
		{"char never pads", 5, 1, 5},
		{"just below a multiple", 23, 8, 24},
		{"large offset", 8191, 8, 8192},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AlignUp(tc.off, tc.align); got != tc.want {
				t.Fatalf("AlignUp(%d, %d) = %d, want %d", tc.off, tc.align, got, tc.want)
			}
		})
	}
}

func TestP1_1_AlignUp_Exhaustive(t *testing.T) {
	for _, a := range []int{1, 2, 4, 8} {
		for off := 0; off < 200; off++ {
			got := AlignUp(off, a)
			if got%a != 0 || got < off || got-off >= a {
				t.Fatalf("AlignUp(%d, %d) = %d: not the smallest multiple of %d >= %d", off, a, got, a, off)
			}
		}
	}
}

func TestP1_2_AppendFixed(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		v    types.Value
		want string
	}{
		{"bool true", nil, types.Bool(true), "01"},
		{"bool false", nil, types.Bool(false), "00"},
		{"int4 7", nil, types.Int32(7), "07000000"},
		{"int4 -2 two's complement", nil, types.Int32(-2), "feffffff"},
		{"int4 min", nil, types.Int32(math.MinInt32), "00000080"},
		{"int8 100", nil, types.Int64(100), "6400000000000000"},
		{"int8 -1", nil, types.Int64(-1), "ffffffffffffffff"},
		{"int8 little-endian order", nil, types.Int64(0x0102030405060708), "0807060504030201"},
		{"float8 1.5", nil, types.Float64(1.5), "000000000000f83f"},
		{"float8 -0.0 keeps its sign bit", nil, types.Float64(math.Copysign(0, -1)), "0000000000000080"},
		{"appends after existing bytes", []byte{0xaa}, types.Bool(true), "aa01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AppendFixed(tc.buf, tc.v)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			assertBytes(t, "AppendFixed", got, mustHex(tc.want))
		})
	}
}

func TestP1_2_AppendFixed_NotFixed(t *testing.T) {
	for _, v := range []types.Value{types.Null(), types.Text("hi")} {
		got, err := AppendFixed([]byte{0xaa}, v)
		if !errors.Is(err, ErrNotFixed) {
			t.Fatalf("AppendFixed(%v) err = %v, want ErrNotFixed", v, err)
		}
		assertBytes(t, "buf after error", got, []byte{0xaa})
	}
}

func TestP1_3_DecodeFixed(t *testing.T) {
	tests := []struct {
		name    string
		kind    types.Kind
		src     string
		want    types.Value
		wantErr error
	}{
		{"int4 -2", types.KindInt32, "feffffff", types.Int32(-2), nil},
		{"int4 ignores trailing bytes", types.KindInt32, "07000000ffff", types.Int32(7), nil},
		{"int8 100", types.KindInt64, "6400000000000000", types.Int64(100), nil},
		{"int8 min", types.KindInt64, "0000000000000080", types.Int64(math.MinInt64), nil},
		{"float8 1.5 with trailing byte", types.KindFloat64, "000000000000f83f99", types.Float64(1.5), nil},
		{"bool 0 is false", types.KindBool, "00", types.Bool(false), nil},
		{"bool 1 is true", types.KindBool, "01", types.Bool(true), nil},
		{"bool any nonzero is true", types.KindBool, "7f", types.Bool(true), nil},
		{"int8 short buffer", types.KindInt64, "010203", types.Value{}, ErrShortBuffer},
		{"bool empty buffer", types.KindBool, "", types.Value{}, ErrShortBuffer},
		{"text is not fixed", types.KindText, "07686900", types.Value{}, ErrNotFixed},
		{"null is not fixed", types.KindNull, "00", types.Value{}, ErrNotFixed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeFixed(tc.kind, mustHex(tc.src))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got.Kind != tc.want.Kind || !types.Identical(got, tc.want) {
				t.Fatalf("got %v (%s), want %v (%s)", got, got.Kind, tc.want, tc.want.Kind)
			}
		})
	}
}

func TestP1_3_DecodeFixed_RoundTrip(t *testing.T) {
	vals := []types.Value{
		types.Bool(true), types.Bool(false),
		types.Int32(0), types.Int32(math.MaxInt32), types.Int32(math.MinInt32), types.Int32(-123456),
		types.Int64(math.MaxInt64), types.Int64(math.MinInt64), types.Int64(-9876543210),
		types.Float64(math.Pi), types.Float64(-1e300), types.Float64(math.Inf(-1)), types.Float64(5e-324),
	}
	for _, v := range vals {
		b, err := AppendFixed(nil, v)
		if err != nil {
			t.Fatalf("AppendFixed(%v): %v", v, err)
		}
		if len(b) != AttLen(v.Kind) {
			t.Fatalf("AppendFixed(%v) wrote %d bytes, want %d", v, len(b), AttLen(v.Kind))
		}
		got, err := DecodeFixed(v.Kind, b)
		if err != nil || got.Kind != v.Kind || !types.Identical(got, v) {
			t.Fatalf("round trip %v → % x → %v (err %v)", v, b, got, err)
		}
	}
}

func TestP1_4_AppendVarlena(t *testing.T) {
	a126 := strings.Repeat("a", 126)
	a127 := strings.Repeat("a", 127)
	tests := []struct {
		name    string
		buf     []byte
		s       string
		wantHdr string // expected header bytes; the string bytes must follow
	}{
		{"short 'hi': (3<<1)|1", nil, "hi", "07"},
		{"empty string is 1 byte", nil, "", "03"},
		{"single byte", nil, "x", "05"},
		{"126 bytes: total 127 still short", nil, a126, "ff"},
		{"127 bytes: total 131 needs 4-byte header", nil, a127, "0c020000"},
		{"130 bytes: total 134", nil, strings.Repeat("x", 130), "18020000"},
		{"1000 bytes: total 1004", nil, strings.Repeat("z", 1000), "b00f0000"},
		{"appends after existing bytes", []byte{0xaa, 0xbb}, "hi", "aabb07"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AppendVarlena(tc.buf, tc.s)
			want := append(mustHex(tc.wantHdr), tc.s...)
			assertBytes(t, "AppendVarlena", got, want)
		})
	}
}

func TestP1_5_DecodeVarlena(t *testing.T) {
	tests := []struct {
		name    string
		src     []byte
		want    string
		wantN   int
		wantErr error
	}{
		{"short 'hi' ignores trailing", mustHex("076869ff"), "hi", 3, nil},
		{"short empty", mustHex("03"), "", 1, nil},
		{"4-byte header for a small value", mustHex("180000006869"), "hi", 6, nil},
		{"4-byte header empty value", mustHex("10000000"), "", 4, nil},
		{"short header 0xff = 126 bytes", append([]byte{0xff}, strings.Repeat("a", 126)...), strings.Repeat("a", 126), 127, nil},
		{"4-byte header first byte 0x00 (total 64)", append(mustHex("00010000"), strings.Repeat("q", 60)...), strings.Repeat("q", 60), 64, nil},
		{"empty input", nil, "", 0, ErrShortBuffer},
		{"short header longer than buffer", mustHex("0968"), "", 0, ErrShortBuffer},
		{"4-byte header needs 4 bytes", mustHex("1800"), "", 0, ErrShortBuffer},
		{"4-byte total beyond buffer", mustHex("1800000068"), "", 0, ErrShortBuffer},
		{"0x01 is a TOAST pointer tag", mustHex("01"), "", 0, ErrBadVarlena},
		{"4-byte total smaller than header", mustHex("08000000"), "", 0, ErrBadVarlena},
		{"compressed form (bits 10)", mustHex("02000000"), "", 0, ErrBadVarlena},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, n, err := DecodeVarlena(tc.src)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if s != tc.want || n != tc.wantN {
				t.Fatalf("got (%q, %d), want (%q, %d)", s, n, tc.want, tc.wantN)
			}
		})
	}
}

func TestP1_5_DecodeVarlena_RoundTrip(t *testing.T) {
	for n := 0; n <= 300; n++ {
		s := strings.Repeat("\x00b", n/2) + strings.Repeat("c", n%2)
		enc := AppendVarlena(nil, s)
		got, used, err := DecodeVarlena(append(enc, 0xee, 0xee))
		if err != nil || got != s || used != len(enc) {
			t.Fatalf("len %d: got (%d bytes, used %d, err %v), want (%d bytes, used %d)", n, len(got), used, err, len(s), len(enc))
		}
	}
}

func TestP1_6_NullBitmap(t *testing.T) {
	tests := []struct {
		name string
		row  types.Row
		want []byte
	}{
		{"no NULLs → no bitmap", testkit.R(int32(7), true), nil},
		{"empty row → no bitmap", testkit.R(), nil},
		{"sample row B", rowB(), []byte{0x1a}},
		{"all NULL", testkit.R(nil, nil, nil), []byte{0x00}},
		{"only last NULL", testkit.R(1, 2, nil), []byte{0x03}},
		{"8 values, one NULL fills exactly one byte", testkit.R(1, 1, 1, 1, 1, 1, 1, nil), []byte{0x7f}},
		{"10 values, NULL at 0 and 9", testkit.R(nil, 1, 1, 1, 1, 1, 1, 1, 1, nil), []byte{0xfe, 0x01}},
		{"9 values, only 8 present", testkit.R(nil, nil, nil, nil, nil, nil, nil, nil, 1), []byte{0x00, 0x01}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NullBitmap(tc.row)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got % x, want nil", got)
				}
				return
			}
			assertBytes(t, "bitmap", got, tc.want)
		})
	}
}

func TestP1_7_AttIsNull(t *testing.T) {
	tests := []struct {
		name   string
		bitmap []byte
		i      int
		want   bool
	}{
		{"0x1a attr 0 NULL", []byte{0x1a}, 0, true},
		{"0x1a attr 1 present", []byte{0x1a}, 1, false},
		{"0x1a attr 2 NULL", []byte{0x1a}, 2, true},
		{"0x1a attr 3 present", []byte{0x1a}, 3, false},
		{"0x1a attr 4 present", []byte{0x1a}, 4, false},
		{"second byte attr 8 present", []byte{0xfe, 0x01}, 8, false},
		{"second byte attr 9 NULL", []byte{0xfe, 0x01}, 9, true},
		{"nil bitmap means no NULLs", nil, 3, false},
		{"empty bitmap means no NULLs", []byte{}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AttIsNull(tc.bitmap, tc.i); got != tc.want {
				t.Fatalf("AttIsNull(% x, %d) = %v, want %v", tc.bitmap, tc.i, got, tc.want)
			}
		})
	}
}

func TestP1_7_AttIsNull_MatchesNullBitmap(t *testing.T) {
	row := testkit.R(nil, 1, nil, nil, 2, 3, nil, 4, 5, nil, nil, 6, 7, 8, 9, nil, 10)
	bm := NullBitmap(row)
	for i, v := range row {
		if got := AttIsNull(bm, i); got != v.IsNull() {
			t.Fatalf("attr %d: AttIsNull = %v, want %v", i, got, v.IsNull())
		}
	}
}

func TestP1_8_AppendHeader(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		h    TupleHeader
		want string
	}{
		{"sample row A header", nil, TupleHeader{Xmin: 42, Infomask: HeapHasVarWidth, Natts: 5, Hoff: 16},
			"2a000000 00000000 0200 0500 10"},
		{"all fields set", nil, TupleHeader{Xmin: 0x01020304, Xmax: 0xa0b0c0d0, Infomask: HeapHasNull | HeapHasVarWidth, Natts: 300, Hoff: 24},
			"04030201 d0c0b0a0 0300 2c01 18"},
		{"zero header", nil, TupleHeader{}, "00000000 00000000 0000 0000 00"},
		{"appends after existing bytes", []byte{0xee}, TupleHeader{Xmin: 1, Natts: 1, Hoff: 16},
			"ee 01000000 00000000 0000 0100 10"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AppendHeader(tc.buf, tc.h)
			assertBytes(t, "header", got, mustHex(tc.want))
		})
	}
}

func TestP1_9_DecodeHeader(t *testing.T) {
	pad := func(b []byte, n int) []byte { return append(b, make([]byte, n)...) }
	tests := []struct {
		name    string
		data    []byte
		want    TupleHeader
		wantErr error
	}{
		{"golden row A", golden48(), TupleHeader{Xmin: 42, Infomask: HeapHasVarWidth, Natts: 5, Hoff: 16}, nil},
		{"golden row B", golden168(), TupleHeader{Xmin: 7, Infomask: HeapHasNull | HeapHasVarWidth, Natts: 5, Hoff: 16}, nil},
		{"header + 3 pad bytes only", pad(mustHex("2a000000 00000000 0200 0500 10"), 3),
			TupleHeader{Xmin: 42, Infomask: HeapHasVarWidth, Natts: 5, Hoff: 16}, nil},
		{"xmax and large natts", pad(mustHex("04030201 d0c0b0a0 0100 1900 18"), 11),
			TupleHeader{Xmin: 0x01020304, Xmax: 0xa0b0c0d0, Infomask: HeapHasNull, Natts: 25, Hoff: 24}, nil},
		{"12 bytes is too short", mustHex("2a000000 00000000 0200 0500"), TupleHeader{}, ErrShortBuffer},
		{"hoff not multiple of 8", pad(mustHex("2a000000 00000000 0000 0100 0e"), 1), TupleHeader{}, ErrCorrupt},
		{"hoff smaller than header", pad(mustHex("2a000000 00000000 0000 0100 08"), 3), TupleHeader{}, ErrCorrupt},
		{"hoff leaves no room for the bitmap", pad(mustHex("2a000000 00000000 0100 1900 10"), 3), TupleHeader{}, ErrCorrupt},
		{"hoff past end of data", pad(mustHex("2a000000 00000000 0000 0100 18"), 3), TupleHeader{}, ErrCorrupt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeHeader(tc.data)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestP1_9_DecodeHeader_RoundTrip(t *testing.T) {
	for _, h := range []TupleHeader{
		{Xmin: 1, Natts: 1, Hoff: 16},
		{Xmin: 99, Xmax: 100, Infomask: HeapHasNull, Natts: 64, Hoff: 24},
		{Xmin: 0xffffffff, Xmax: 0xfffffffe, Infomask: HeapHasNull | HeapHasVarWidth, Natts: 24, Hoff: 16},
	} {
		data := AppendHeader(nil, h)
		data = append(data, make([]byte, int(h.Hoff)-HeaderSize)...)
		got, err := DecodeHeader(data)
		if err != nil || got != h {
			t.Fatalf("round trip %+v → %+v (err %v)", h, got, err)
		}
	}
}

func TestP1_4_AppendVarlena_HeaderBits(t *testing.T) {
	for n := 0; n <= 300; n++ {
		s := strings.Repeat("v", n)
		got := AppendVarlena(nil, s)
		if n <= 126 {
			if len(got) != n+1 || got[0]&1 != 1 || int(got[0]>>1) != n+1 {
				t.Fatalf("len %d: header % x, want 1-byte header with total %d", n, got[:1], n+1)
			}
			continue
		}
		if len(got) != n+4 || got[0]&3 != 0 || int(binary.LittleEndian.Uint32(got)>>2) != n+4 {
			t.Fatalf("len %d: header % x, want 4-byte header with total %d", n, got[:4], n+4)
		}
	}
}

func TestP1_4_AppendVarlena_Boundary(t *testing.T) {
	tests := []struct {
		n, wantLen int
	}{
		{125, 126}, {126, 127}, {127, 131}, {128, 132},
	}
	for _, tc := range tests {
		if got := len(AppendVarlena(nil, strings.Repeat("a", tc.n))); got != tc.wantLen {
			t.Fatalf("len(AppendVarlena(%d bytes)) = %d, want %d", tc.n, got, tc.wantLen)
		}
	}
}

func TestP1_5_DecodeVarlena_EveryFirstByte(t *testing.T) {
	buf := make([]byte, 200)
	for b := 0; b < 256; b++ {
		buf[0] = byte(b)
		_, n, err := DecodeVarlena(buf[:1+127])
		switch {
		case b == 0x01:
			if !errors.Is(err, ErrBadVarlena) {
				t.Fatalf("first byte %#02x: err = %v, want ErrBadVarlena", b, err)
			}
		case b&1 == 1:
			if err != nil || n != b>>1 {
				t.Fatalf("first byte %#02x: n = %d err = %v, want n = %d", b, n, err, b>>1)
			}
		case b&3 == 2:
			if !errors.Is(err, ErrBadVarlena) {
				t.Fatalf("first byte %#02x: err = %v, want ErrBadVarlena", b, err)
			}
		}
	}
}

func TestP1_8_AppendHeader_FieldOffsets(t *testing.T) {
	h := TupleHeader{Xmin: 1000, Xmax: 2000, Infomask: HeapHasNull, Natts: 7, Hoff: 16}
	got := AppendHeader(make([]byte, 0, 64), h)
	if len(got) != HeaderSize {
		t.Fatalf("len = %d, want %d", len(got), HeaderSize)
	}
	if v := binary.LittleEndian.Uint32(got[0:]); v != 1000 {
		t.Fatalf("xmin at [0:4) = %d, want 1000", v)
	}
	if v := binary.LittleEndian.Uint32(got[4:]); v != 2000 {
		t.Fatalf("xmax at [4:8) = %d, want 2000", v)
	}
	if v := binary.LittleEndian.Uint16(got[8:]); v != HeapHasNull {
		t.Fatalf("infomask at [8:10) = %d, want 1", v)
	}
	if v := binary.LittleEndian.Uint16(got[10:]); v != 7 {
		t.Fatalf("natts at [10:12) = %d, want 7", v)
	}
	if got[12] != 16 {
		t.Fatalf("hoff at [12] = %d, want 16", got[12])
	}
}

func TestP1_8_AppendHeader_Repeated(t *testing.T) {
	var buf []byte
	for i := 1; i <= 3; i++ {
		buf = AppendHeader(buf, TupleHeader{Xmin: types.TxnID(i), Natts: uint16(i), Hoff: 16})
	}
	if len(buf) != 3*HeaderSize {
		t.Fatalf("len = %d, want %d", len(buf), 3*HeaderSize)
	}
	for i := 0; i < 3; i++ {
		if x := binary.LittleEndian.Uint32(buf[i*HeaderSize:]); x != uint32(i+1) {
			t.Fatalf("header %d xmin = %d, want %d", i, x, i+1)
		}
	}
}

func TestP1_9_DecodeHeader_NoNullBitmapNeeded(t *testing.T) {
	// Without HASNULL, natts=200 needs no bitmap: hoff 16 is fine.
	data := append(mustHex("01000000 00000000 0000 c800 10"), make([]byte, 3)...)
	h, err := DecodeHeader(data)
	if err != nil || h.Natts != 200 || h.Hoff != 16 {
		t.Fatalf("got %+v err %v, want natts 200 hoff 16", h, err)
	}
	// The same header WITH HASNULL needs 13+25 = 38 → hoff 16 is corrupt.
	data[8] = 0x01
	if _, err := DecodeHeader(data); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}
