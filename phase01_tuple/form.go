package tuple

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// TupleDesc is a schema plus the per-column offset cache Postgres keeps in
// its tuple descriptor (attcacheoff). Create it with NewTupleDesc.
type TupleDesc struct {
	Schema types.Schema
	// AttCacheOff[j] is the byte offset of column j measured from the start of
	// the data area (hoff), valid for every tuple with no NULL before column j.
	// -1 means "not cached (yet)". Filled lazily by GetAttr (Problem 1.13).
	AttCacheOff []int
	// Stats counts how GetAttr located non-NULL attributes.
	Stats GetAttrStats
}

// GetAttrStats is the instrumentation used by the tests of Problem 1.13.
type GetAttrStats struct {
	FastPath int // attribute found via AttCacheOff, without walking earlier columns
	SlowPath int // attribute found by walking columns 0..col
}

// NewTupleDesc returns a descriptor with an empty offset cache (all -1).
func NewTupleDesc(s types.Schema) *TupleDesc {
	off := make([]int, s.Len())
	for i := range off {
		off[i] = -1
	}
	return &TupleDesc{Schema: s, AttCacheOff: off}
}

// Problem 1.10 — ComputeTupleSize [Medium]
//
// Compute the exact byte length FormTuple will produce for a row, without forming it.
//
// A formed tuple is: header (13 bytes) + null bitmap (only if some value is
// NULL) + zero padding up to hoff + every non-NULL attribute in column order,
// each preceded by alignment padding. Offsets are measured from the start of
// the tuple (hoff is a multiple of 8, so this matches Postgres' absolute
// alignment of a MAXALIGNed tuple).
//
//	hoff = AlignUp(HeaderSize + bitmap, 8)   bitmap = BitmapLen(len(row)) if any NULL else 0
//	for each column i with a non-NULL value:
//	   fixed width:  off = AlignUp(off, AttAlign(kind)); off += AttLen(kind)
//	   text, 1+len(s) <= 127:  off += 1+len(s)        (short varlena: NOT aligned)
//	   text, otherwise:        off = AlignUp(off, 4); off += 4+len(s)
//	NULL values take no space at all (only their bitmap bit).
//
// Rules:
//  1. Column kinds come from the schema; the row is assumed valid
//     (FormTuple validates). Return the final offset = total length.
//  2. No trailing padding after the last attribute.
//
// Example (schema id int4, active bool, score float8, name text, big int8):
//
//	(7, true, 1.5, 'hi', 100)              → 48
//	  hoff 16 | id 16-20 | active 20 | pad 21-23 | score 24-32 | 'hi' 32-35 | pad | big 40-48
//	(NULL, false, NULL, 130×'x', -1)       → 168
//	  hoff 16 | active 16 | pad 17-19 | text 20-154 (4-byte header) | pad | big 160-168
//	(NULL, NULL, NULL, NULL, NULL)         → 16
//
// Postgres reference: heap_compute_data_size() in src/backend/access/common/heaptuple.c
func ComputeTupleSize(schema types.Schema, row types.Row) int {
	panic("TODO: Problem 1.10")
}

// Problem 1.11 — FormTuple [Hard]
//
// Serialize a row into a heap tuple: header + optional null bitmap + aligned attributes.
//
//	+--------------+--------+-----+------+-----+------+-----+------+
//	| header (13)  | bitmap | pad | att0 | pad | att1 | ... | attN |
//	+--------------+--------+-----+------+-----+------+-----+------+
//	0              13            hoff
//
// Rules:
//  1. Validate first, nothing is returned on error:
//     len(row) != schema.Len() → ErrArity; then for each column in order:
//     NULL in a NotNull column → ErrNotNull; a non-NULL value whose Kind
//     differs from the column Kind (e.g. Int64 in an int4 column) → ErrTypeMismatch.
//  2. Header: Xmin = xmin, Xmax = 0, Natts = len(row),
//     Hoff = AlignUp(HeaderSize + bitmap, 8) (bitmap bytes only if a NULL exists),
//     Infomask = HeapHasNull if any NULL | HeapHasVarWidth if at least one
//     NON-NULL text value is stored.
//  3. Bitmap (Problem 1.6) right after the header, then zero bytes up to hoff.
//  4. Attributes exactly as described in Problem 1.10: fixed-width values via
//     AppendFixed after aligning; text via AppendVarlena — short headers are
//     not aligned, 4-byte headers are aligned to 4. All padding bytes are 0x00.
//  5. len(result) == ComputeTupleSize(schema, row).
//
// Example (schema id int4, active bool, score float8, name text, big int8):
//
//	FormTuple(s, (7, true, 1.5, 'hi', 100), 42) → 48 bytes
//	  00: 2a 00 00 00 00 00 00 00 02 00 05 00 10 00 00 00   header, pad to 16
//	  16: 07 00 00 00 01 00 00 00 00 00 00 00 00 00 f8 3f   id, active, pad, score
//	  32: 07 68 69 00 00 00 00 00 64 00 00 00 00 00 00 00   'hi', pad, big
//	FormTuple(s, (NULL, false, NULL, 130×'x', -1), 7) → 168 bytes:
//	  infomask 0x0003, bitmap 0x1a at byte 13, active=00 at 16, pad 17-19,
//	  header 18 02 00 00 at 20, 130×'x' at 24-153, pad 154-159, ff×8 at 160.
//
// Constraints: allocate the output once (size it with ComputeTupleSize).
// Postgres reference: heap_form_tuple() / heap_fill_tuple() in src/backend/access/common/heaptuple.c
func FormTuple(schema types.Schema, row types.Row, xmin types.TxnID) ([]byte, error) {
	panic("TODO: Problem 1.11")
}

// Problem 1.12 — DeformTuple [Hard]
//
// Parse a heap tuple produced by FormTuple back into a row.
//
// Walk the attributes in order, keeping a running offset that starts at hoff.
// Fixed-width attributes: align the offset to the type, then read AttLen bytes.
// Text is the tricky one: you cannot know from the schema whether the value
// was stored with a 1-byte header (unaligned) or a 4-byte header (aligned to
// 4). Postgres' trick: padding bytes are always 0x00, and a 1-byte header is
// always odd (bit0 = 1), so
//
//	if data[off] == 0x00 → it is padding: off = AlignUp(off, 4) (4-byte header)
//	else                 → a varlena header starts right here
//
// (A 4-byte header's first byte may be 0x00 too, but it always sits on an
// aligned offset, where AlignUp is a no-op.)
//
// Rules:
//  1. Decode the header with DecodeHeader (its errors are returned as-is).
//  2. natts > schema.Len() → ErrCorrupt. natts < schema.Len() is legal:
//     the table got columns added later (ALTER TABLE ADD COLUMN), the
//     missing trailing attributes read as NULL.
//  3. The bitmap is data[13 : 13+BitmapLen(natts)] when HeapHasNull is set;
//     otherwise there are no NULLs. NULL attributes take no bytes.
//  4. Values get the column's exact Kind (int4 → types.Int32, …).
//  5. Any attribute that runs past the end of data, or has an invalid varlena
//     header → ErrCorrupt. Bytes after the last attribute are ignored.
//
// Example:
//
//	DeformTuple(s, <the 48-byte tuple from Problem 1.11>) → (7, true, 1.5, 'hi', 100)
//	tuple formed with 3 columns (1, 'a', 2.5), read with a 5-column schema
//	  → (1, 'a', 2.5, NULL, NULL)
//	the 48-byte tuple truncated to 40 bytes → ErrCorrupt
//
// Postgres reference: heap_deform_tuple() in src/backend/access/common/heaptuple.c,
// att_align_pointer() in src/include/access/tupmacs.h
func DeformTuple(schema types.Schema, data []byte) (types.Row, error) {
	panic("TODO: Problem 1.12")
}

// Problem 1.13 — GetAttr [Medium]
//
// Fetch ONE attribute of a tuple, using cached column offsets to skip the walk when possible.
//
// Deforming a whole tuple to read column 40 is wasteful. Postgres caches, per
// column, the offset at which it always lives when nothing before it can vary
// in size (attcacheoff). With the cache, reading a column is one jump.
//
// The cache (td.AttCacheOff, offsets relative to hoff):
//   - AttCacheOff[0] = 0 for any kind (the first column always starts at hoff).
//   - For j >= 1 it is cacheable iff columns 0..j-1 AND column j are all
//     fixed-width (no text): AttCacheOff[j] = AlignUp(AttCacheOff[j-1] +
//     AttLen(kind j-1), AttAlign(kind j)). Every other entry stays -1.
//   - On every call with a valid col, fill all cacheable entries for
//     columns 0..col that are still -1 (before looking at the tuple).
//
// Rules:
//  1. col < 0 or col >= schema.Len() → ErrBadAttnum (cache untouched).
//  2. Fill the cache as above, then decode the header (DecodeHeader errors as-is);
//     natts > schema.Len() → ErrCorrupt.
//  3. col >= natts, or the bitmap marks col NULL → return NULL; no stats change.
//  4. Fast path: if AttCacheOff[col] >= 0 and no column before col is NULL,
//     read the value at hoff + AttCacheOff[col]; td.Stats.FastPath++.
//  5. Otherwise walk columns 0..col exactly like DeformTuple; td.Stats.SlowPath++.
//  6. The result always equals DeformTuple(schema, data)[col]; read failures → ErrCorrupt.
//
// Example (schema id int4, active bool, score float8, name text, big int8):
//
//	td := NewTupleDesc(s)                  // AttCacheOff = [-1 -1 -1 -1 -1]
//	td.GetAttr(<(7,true,1.5,'hi',100)>, 1) → true,  fast; cache [0 4 -1 -1 -1]
//	td.GetAttr(same, 4)                    → 100,   slow; cache [0 4 8 -1 -1]
//	td.GetAttr(same, 2)                    → 1.5,   fast
//	td.GetAttr(<(NULL,false,NULL,130×'x',-1)>, 0) → NULL, no stats change
//	td.GetAttr(<same NULL row>, 1)         → false, slow (a NULL precedes it)
//
// Constraints: the fast path must not look at any column other than col.
// Postgres reference: nocachegetattr() in src/backend/access/common/heaptuple.c
func (td *TupleDesc) GetAttr(data []byte, col int) (types.Value, error) {
	panic("TODO: Problem 1.13")
}
