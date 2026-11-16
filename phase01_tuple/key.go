package tuple

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// Key-encoding tag bytes (ascending form; DESC columns store them inverted).
const (
	KeyTagValue byte = 0x01 // a non-NULL value follows
	KeyTagNull  byte = 0x02 // NULL: sorts after every value in ASC
)

// Problem 1.14 — EncodeKey [Hard]
//
// Encode a list of values into "memcomparable" bytes whose bytes.Compare order equals SQL ORDER BY.
//
// A B+Tree that compares raw byte strings is fast and type-agnostic. To use it
// for SQL keys, each value is encoded so that comparing the encodings with
// bytes.Compare gives the same answer as comparing the values. Columns are
// concatenated, so every column's encoding must also be prefix-free.
//
// Per column, in this order:
//
//	tag:   NULL → 0x02 and nothing else.  Non-NULL → 0x01 then the payload:
//	bool:   0x00 false, 0x01 true
//	int4:   4 bytes BIG-endian of uint32(int32) ^ 0x80000000          (flip sign bit)
//	int8:   8 bytes BIG-endian of uint64(int64) ^ 0x8000000000000000
//	float8: b = Float64bits(f); if sign bit set: b = ^b  else: b |= 1<<63;
//	        8 bytes BIG-endian. Before that, -0.0 becomes +0.0 and every NaN
//	        becomes math.NaN() (so NaN sorts above +Inf, like Postgres).
//	text:   each byte copied, except 0x00 which becomes 0x00 0xFF;
//	        then the terminator 0x00 0x01.
//	DESC column: invert (XOR 0xFF) every byte of that column's encoding,
//	        tag included — so NULLs come FIRST for DESC (Postgres' default).
//
// Rules:
//  1. desc == nil means every column is ascending; otherwise
//     len(desc) != len(values) → ErrArity.
//  2. Columns compared with each other must have the same Kind at the same
//     position (an int4 key column holds only int4 values).
//  3. For two keys a, b of the same layout: sign(bytes.Compare(EncodeKey(a),
//     EncodeKey(b))) == sign(ORDER BY comparison), where per column non-NULL
//     values use types.Compare, ASC puts NULL last, DESC reverses the order
//     and puts NULL first; equal keys produce identical bytes.
//
// Example:
//
//	EncodeKey((Int32(1)), nil)         → 01 80 00 00 01
//	EncodeKey((Int32(-1)), nil)        → 01 7f ff ff ff
//	EncodeKey((Text("a\x00b")), nil)   → 01 61 00 ff 62 00 01
//	EncodeKey((Float64(1.5)), nil)     → 01 bf f8 00 00 00 00 00 00
//	EncodeKey((NULL), nil)             → 02
//	EncodeKey((Int32(1), Text("b")), []bool{false, true})
//	                                   → 01 80 00 00 01 fe 9d ff fe
//
// Postgres reference: none directly (Postgres B-trees call type comparators);
// this is the memcomparable format of CockroachDB / MyRocks / FoundationDB tuples.
func EncodeKey(values types.Row, desc []bool) ([]byte, error) {
	panic("TODO: Problem 1.14")
}
