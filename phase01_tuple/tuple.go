// Package tuple turns a row into bytes on disk the way Postgres heap tuples do:
// a fixed header, an optional null bitmap, then every non-NULL attribute
// aligned to its type's alignment. Little-endian everywhere.
package tuple

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/natthphong/daTarbase/pkg/types"
)

// HeaderSize is the size of our fixed tuple header in bytes (see Problem 1.8).
//
//	offset 0  xmin     uint32  inserting transaction
//	offset 4  xmax     uint32  deleting transaction (0 = live)
//	offset 8  infomask uint16  flag bits (HeapHasNull, HeapHasVarWidth)
//	offset 10 natts    uint16  number of attributes stored in the tuple
//	offset 12 hoff     uint8   offset of the first attribute byte
const HeaderSize = 13

// Infomask flag bits.
const (
	HeapHasNull     uint16 = 0x0001 // tuple has a null bitmap (at least one NULL)
	HeapHasVarWidth uint16 = 0x0002 // tuple stores at least one non-NULL varlena (text)
)

// Varlena limits.
const (
	VarlenaShortMax = 127 // max TOTAL size (header byte included) of a 1-byte-header varlena
	VarlenaLongHdr  = 4   // size of the 4-byte varlena header
)

var (
	ErrShortBuffer  = errors.New("tuple: buffer too short")
	ErrCorrupt      = errors.New("tuple: corrupt tuple")
	ErrBadVarlena   = errors.New("tuple: invalid varlena header")
	ErrNotFixed     = errors.New("tuple: kind is not fixed-width")
	ErrArity        = errors.New("tuple: wrong number of values")
	ErrTypeMismatch = errors.New("tuple: value kind does not match column kind")
	ErrNotNull      = errors.New("tuple: NULL in NOT NULL column")
	ErrBadAttnum    = errors.New("tuple: attribute number out of range")
)

// AttLen is the on-disk length of a type (Postgres: typlen). Fixed-width types
// return their byte size; text (a varlena) returns -1. Other kinds return 0.
func AttLen(k types.Kind) int {
	switch k {
	case types.KindBool:
		return 1
	case types.KindInt32:
		return 4
	case types.KindInt64, types.KindFloat64:
		return 8
	case types.KindText:
		return -1
	}
	return 0
}

// AttAlign is the alignment requirement of a type (Postgres: typalign):
// bool 'c'=1, int4 'i'=4, int8/float8 'd'=8, text 'i'=4. Other kinds return 1.
func AttAlign(k types.Kind) int {
	switch k {
	case types.KindInt32, types.KindText:
		return 4
	case types.KindInt64, types.KindFloat64:
		return 8
	}
	return 1
}

// BitmapLen is the number of bytes of a null bitmap for natts attributes.
func BitmapLen(natts int) int { return (natts + 7) / 8 }

// TupleHeader is the decoded fixed part of a tuple (Postgres: HeapTupleHeaderData).
type TupleHeader struct {
	Xmin     types.TxnID // transaction that inserted this version
	Xmax     types.TxnID // transaction that deleted it, 0 if live
	Infomask uint16      // HeapHasNull | HeapHasVarWidth
	Natts    uint16      // number of attributes physically stored
	Hoff     uint8       // offset of user data: header + bitmap, rounded up to 8
}

// Problem 1.1 — AlignUp [Easy]
//
// Round an offset up to the next multiple of a type's alignment (typalign padding).
//
// Postgres never stores an int4 at an odd address: before writing an
// attribute it pads the offset up to the type's alignment —
// 'c' char = 1, 's' short = 2, 'i' int = 4, 'd' double = 8.
//
//	offset:   20 21 22 23 24
//	         [b ][pad pad pad][float8 ...]   bool ends at 21, float8 wants 24
//
// Rules:
//  1. Return the smallest multiple of align that is >= off.
//  2. off >= 0; align is a power of two >= 1 (1, 2, 4 or 8). An already aligned
//     offset is returned unchanged; align 1 never changes anything.
//
// Example:
//
//	AlignUp(13, 8) == 16   AlignUp(16, 8) == 16   AlignUp(21, 4) == 24
//	AlignUp(5, 2)  == 6    AlignUp(5, 1)  == 5    AlignUp(0, 8)  == 0
//
// Constraints: O(1), no loops. Hint: align-1 is a bit mask.
// Postgres reference: TYPEALIGN() / att_align_nominal() in src/include/c.h, access/tupmacs.h
func AlignUp(off, align int) int {
	return (off + align - 1) &^ (align - 1)
}

// Problem 1.2 — AppendFixed [Easy]
//
// Append the on-disk bytes of a fixed-width value (bool, int4, int8, float8) to buf.
//
// Rules:
//  1. bool   → 1 byte: 0x00 false, 0x01 true.
//  2. int4   → 4 bytes little-endian two's complement of int32(v.I).
//  3. int8   → 8 bytes little-endian two's complement of v.I.
//  4. float8 → 8 bytes little-endian of the IEEE-754 bit pattern (math.Float64bits).
//  5. No alignment/padding is added — the caller aligns.
//  6. Any other kind (NULL, text) → return buf unchanged and ErrNotFixed.
//
// Example:
//
//	AppendFixed(nil, Int32(7))     → 07 00 00 00
//	AppendFixed(nil, Int32(-2))    → fe ff ff ff
//	AppendFixed(nil, Float64(1.5)) → 00 00 00 00 00 00 f8 3f
//	AppendFixed([]byte{0xaa}, Bool(true)) → aa 01
//
// Constraints: append only, no extra allocation beyond append's growth.
// Postgres reference: store_att_byval() in src/include/access/tupmacs.h
func AppendFixed(buf []byte, v types.Value) ([]byte, error) {
	switch v.Kind {
	case types.KindBool:
		if v.B {
			return append(buf, 1), nil
		}
		return append(buf, 0), nil
	case types.KindInt32:
		return binary.LittleEndian.AppendUint32(buf, uint32(int32(v.I))), nil
	case types.KindInt64:
		return binary.LittleEndian.AppendUint64(buf, uint64(v.I)), nil
	case types.KindFloat64:
		return binary.LittleEndian.AppendUint64(buf, math.Float64bits(v.F)), nil
	}
	return buf, ErrNotFixed
}

// Problem 1.3 — DecodeFixed [Easy]
//
// Decode a fixed-width value of the given kind from the start of src.
//
// Rules:
//  1. Reads exactly AttLen(kind) bytes from src[0:]; extra bytes are ignored.
//  2. bool: 0x00 → false, any other byte → true. int4 → types.Int32,
//     int8 → types.Int64, float8 → types.Float64 (the exact Kind matters).
//  3. kind is not bool/int4/int8/float8 → ErrNotFixed.
//  4. len(src) < AttLen(kind) → ErrShortBuffer.
//
// Example:
//
//	DecodeFixed(KindInt32, fe ff ff ff)                  → Int32(-2)
//	DecodeFixed(KindFloat64, 00 00 00 00 00 00 f8 3f 99) → Float64(1.5)
//	DecodeFixed(KindInt64, 01 02 03)                     → ErrShortBuffer
//
// Postgres reference: fetch_att() in src/include/access/tupmacs.h
func DecodeFixed(kind types.Kind, src []byte) (types.Value, error) {
	n := AttLen(kind)
	if n <= 0 {
		return types.Value{}, ErrNotFixed
	}
	if len(src) < n {
		return types.Value{}, ErrShortBuffer
	}
	switch kind {
	case types.KindBool:
		return types.Bool(src[0] != 0), nil
	case types.KindInt32:
		return types.Int32(int32(binary.LittleEndian.Uint32(src))), nil
	case types.KindInt64:
		return types.Int64(int64(binary.LittleEndian.Uint64(src))), nil
	default:
		return types.Float64(math.Float64frombits(binary.LittleEndian.Uint64(src))), nil
	}
}

// Problem 1.4 — AppendVarlena [Medium]
//
// Append a text value as a Postgres-style varlena: a length header followed by the bytes.
//
// Postgres stores variable-length values ("varlena") with a header that
// encodes the TOTAL size (header included). Small values get a 1-byte header,
// big ones a 4-byte header. The two forms are told apart by the lowest bit(s)
// of the first byte (little-endian):
//
//	1-byte ("short") header:  bit0 = 1          byte  = (total << 1) | 1
//	                          total = 1 + len(s), only when total <= 127
//	4-byte header:            bits1..0 = 00     uint32 LE = total << 2
//	                          total = 4 + len(s)
//
// Rules:
//  1. If 1+len(s) <= VarlenaShortMax (127, i.e. len(s) <= 126) use the 1-byte header.
//  2. Otherwise use the 4-byte little-endian header (total << 2).
//  3. Then append the raw bytes of s. No padding: aligning a 4-byte header to
//     4 is the caller's job (FormTuple); short headers are never aligned.
//
// Example:
//
//	AppendVarlena(nil, "hi")   → 07 68 69                 (total 3: 3<<1|1 = 0x07)
//	AppendVarlena(nil, "")     → 03                       (total 1)
//	126 × 'a'                  → ff 61 61 …               (total 127: 0xff)
//	127 × 'a'                  → 0c 02 00 00 61 61 …      (total 131: 131<<2 = 0x20c)
//
// Postgres reference: SET_VARSIZE_SHORT / SET_VARSIZE (varatt.h), heap_fill_tuple()
func AppendVarlena(buf []byte, s string) []byte {
	if total := 1 + len(s); total <= VarlenaShortMax {
		buf = append(buf, byte(total<<1|1))
	} else {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(s)+VarlenaLongHdr)<<2)
	}
	return append(buf, s...)
}

// Problem 1.5 — DecodeVarlena [Medium]
//
// Decode the varlena at the start of src; return the string and the total bytes it occupies.
//
// Rules:
//  1. len(src) == 0 → ErrShortBuffer.
//  2. If bit0 of src[0] is 1 it is a 1-byte header: total = src[0] >> 1.
//     total == 0 (byte 0x01, Postgres' TOAST-pointer tag) → ErrBadVarlena.
//  3. Else if bits1..0 of src[0] are 00 it is a 4-byte header: needs 4 bytes
//     (else ErrShortBuffer); total = uint32 LE >> 2; total < 4 → ErrBadVarlena.
//     Accept a 4-byte header even when the value would have fit a short one.
//  4. Else (bits1..0 == 10, Postgres' inline-compressed form) → ErrBadVarlena.
//  5. total > len(src) → ErrShortBuffer.
//  6. Return s = bytes after the header up to total, and n = total.
//     Bytes after total are ignored.
//
// Example:
//
//	DecodeVarlena(07 68 69 ff)        → "hi", 3
//	DecodeVarlena(18 00 00 00 68 69)  → "hi", 6   (4-byte header, total 6)
//	DecodeVarlena(09 68)              → ErrShortBuffer (total 4 > 2 bytes)
//	DecodeVarlena(02 00 00 00)        → ErrBadVarlena
//
// Postgres reference: VARSIZE_ANY() / VARDATA_ANY() in src/include/varatt.h
func DecodeVarlena(src []byte) (string, int, error) {
	if len(src) == 0 {
		return "", 0, ErrShortBuffer
	}
	var hdr, total int
	switch {
	case src[0]&1 == 1:
		hdr, total = 1, int(src[0]>>1)
		if total == 0 {
			return "", 0, ErrBadVarlena
		}
	case src[0]&3 == 0:
		if len(src) < VarlenaLongHdr {
			return "", 0, ErrShortBuffer
		}
		hdr, total = VarlenaLongHdr, int(binary.LittleEndian.Uint32(src)>>2)
		if total < VarlenaLongHdr {
			return "", 0, ErrBadVarlena
		}
	default:
		return "", 0, ErrBadVarlena
	}
	if total > len(src) {
		return "", 0, ErrShortBuffer
	}
	return string(src[hdr:total]), total, nil
}

// Problem 1.6 — NullBitmap [Easy]
//
// Build the null bitmap for a row: bit i set means attribute i is NOT NULL; nil if no NULLs.
//
// Postgres only stores a bitmap when the tuple has at least one NULL. Bit i
// lives in byte i/8 at bit position i%8 (LSB first). A 1 bit means "value
// present", a 0 bit means NULL.
//
//	row (NULL, false, NULL, 'x', -1)  → bits 4..0 = 1 1 0 1 0 → 0x1a
//
// Rules:
//  1. If no value in row is NULL, return nil (no bitmap).
//  2. Otherwise return BitmapLen(len(row)) bytes; unused high bits of the
//     last byte are 0.
//
// Example:
//
//	NullBitmap((NULL, false, NULL, 'x', -1))  → [0x1a]
//	NullBitmap((7, true))                      → nil
//	10 values, NULL at 0 and 9                 → [0xfe, 0x01]
//
// Postgres reference: heap_fill_tuple() bitmap part in src/backend/access/common/heaptuple.c
func NullBitmap(row types.Row) []byte {
	hasNull := false
	for _, v := range row {
		hasNull = hasNull || v.IsNull()
	}
	if !hasNull {
		return nil
	}
	bm := make([]byte, BitmapLen(len(row)))
	for i, v := range row {
		if !v.IsNull() {
			bm[i/8] |= 1 << (i % 8)
		}
	}
	return bm
}

// Problem 1.7 — AttIsNull [Easy]
//
// Report whether attribute i is NULL according to a null bitmap.
//
// Rules:
//  1. An empty/nil bitmap means the tuple has no NULLs → false.
//  2. Otherwise attribute i is NULL iff bit i%8 of byte i/8 is 0.
//  3. The caller guarantees i < 8*len(bitmap) when the bitmap is non-empty.
//
// Example:
//
//	AttIsNull([0x1a], 0) == true   AttIsNull([0x1a], 1) == false
//	AttIsNull([0xfe, 0x01], 9) == false   AttIsNull(nil, 3) == false
//
// Postgres reference: att_isnull() in src/include/access/tupmacs.h
func AttIsNull(bitmap []byte, i int) bool {
	if len(bitmap) == 0 {
		return false
	}
	return bitmap[i/8]&(1<<(i%8)) == 0
}

// Problem 1.8 — AppendHeader [Medium]
//
// Append the 13-byte fixed tuple header to buf.
//
// Layout (little-endian, no padding between fields):
//
//	 0      4      8          10       12     13
//	+------+------+----------+--------+------+
//	| xmin | xmax | infomask | natts  | hoff |   then: null bitmap (if HASNULL),
//	| u32  | u32  |   u16    |  u16   |  u8  |   zero padding up to hoff, data
//	+------+------+----------+--------+------+
//
// Postgres' real header is 23 bytes (it also holds cmin/cmax and ctid); ours
// keeps the parts this course uses. hoff = AlignUp(HeaderSize + bitmap, 8) —
// the header itself does not compute it, it just stores h.Hoff.
//
// Rules:
//  1. Write exactly the 13 bytes above, in that order; nothing else.
//
// Example:
//
//	AppendHeader(nil, TupleHeader{Xmin: 42, Infomask: HeapHasVarWidth, Natts: 5, Hoff: 16})
//	→ 2a 00 00 00  00 00 00 00  02 00  05 00  10
//
// Postgres reference: HeapTupleHeaderData in src/include/access/htup_details.h
func AppendHeader(buf []byte, h TupleHeader) []byte {
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.Xmin))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.Xmax))
	buf = binary.LittleEndian.AppendUint16(buf, h.Infomask)
	buf = binary.LittleEndian.AppendUint16(buf, h.Natts)
	return append(buf, h.Hoff)
}

// Problem 1.9 — DecodeHeader [Medium]
//
// Decode and sanity-check the fixed header at the start of a tuple.
//
// Rules (checked in this order):
//  1. len(data) < HeaderSize → ErrShortBuffer.
//  2. Read the fields with the layout from Problem 1.8.
//  3. hoff % 8 != 0 → ErrCorrupt.
//  4. hoff < HeaderSize + bitmap → ErrCorrupt, where bitmap = BitmapLen(natts)
//     if infomask has HeapHasNull, else 0.
//  5. hoff > len(data) → ErrCorrupt.
//
// Example:
//
//	DecodeHeader(2a 00 00 00 00 00 00 00 02 00 05 00 10 + 3 pad bytes)
//	  → {Xmin:42 Xmax:0 Infomask:2 Natts:5 Hoff:16}
//	HASNULL with natts=25 (4 bitmap bytes, needs 17) but hoff=16 → ErrCorrupt
//
// Postgres reference: HeapTupleHeaderGetNatts / t_hoff in src/include/access/htup_details.h
func DecodeHeader(data []byte) (TupleHeader, error) {
	if len(data) < HeaderSize {
		return TupleHeader{}, ErrShortBuffer
	}
	h := TupleHeader{
		Xmin:     types.TxnID(binary.LittleEndian.Uint32(data[0:])),
		Xmax:     types.TxnID(binary.LittleEndian.Uint32(data[4:])),
		Infomask: binary.LittleEndian.Uint16(data[8:]),
		Natts:    binary.LittleEndian.Uint16(data[10:]),
		Hoff:     data[12],
	}
	min := HeaderSize
	if h.Infomask&HeapHasNull != 0 {
		min += BitmapLen(int(h.Natts))
	}
	if h.Hoff%8 != 0 || int(h.Hoff) < min || int(h.Hoff) > len(data) {
		return TupleHeader{}, ErrCorrupt
	}
	return h, nil
}
