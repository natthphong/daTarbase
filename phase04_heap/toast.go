package heap

import (
	"encoding/binary"

	"github.com/natthphong/daTarbase/pkg/types"
)

const (
	// ToastThreshold: values longer than this many bytes are moved out of
	// line into the TOAST heap (Postgres: TOAST_TUPLE_THRESHOLD ≈ 2032).
	ToastThreshold = 2000

	// ToastChunkSize is the number of value bytes per TOAST chunk
	// (Postgres: TOAST_MAX_CHUNK_SIZE = 1996 with 8 KB pages).
	ToastChunkSize = 1996

	// ToastChunkHeader is the size of the header in front of each chunk:
	// value id (uint32 LE) then sequence number (uint32 LE).
	ToastChunkHeader = 8

	// ToastPointerSize is the size of a stored toast pointer:
	// tag (1) + value id (uint32 LE) + raw size (uint32 LE).
	ToastPointerSize = 9

	// ToastTagInline marks a stored value whose bytes follow inline.
	ToastTagInline byte = 0x00
	// ToastTagExternal marks a stored value that is a pointer into the TOAST heap.
	ToastTagExternal byte = 0x01
)

// Toaster owns a table's TOAST relation (Postgres: pg_toast.pg_toast_<oid>)
// and its index on (chunk_id, chunk_seq).
type Toaster struct {
	Rel         *Heap                  // the TOAST heap holding chunk tuples
	Index       map[uint32][]types.TID // toast index: value id → TIDs of its chunks, in sequence order
	NextValueID uint32                 // id given to the next out-of-line value (Postgres: an OID); starts at 1
}

// NewToaster returns a Toaster with an empty TOAST heap and index.
func NewToaster() *Toaster {
	return &Toaster{Rel: NewHeap(), Index: map[uint32][]types.TID{}, NextValueID: 1}
}

// Problem 4.13 — Toast [Medium]
//
// Encode a column value for storage: small values inline, large ones split into chunks in the TOAST heap behind a 9-byte pointer.
//
// A heap tuple must fit in one 8 KB page, yet a text column can hold 1 GB.
// Postgres solves it with TOAST ("The Oversized-Attribute Storage
// Technique"): a big value is cut into ~2 KB chunks stored as ordinary
// tuples of a side table, and the main tuple keeps only a small pointer.
//
//	inline   (len ≤ 2000):  [0x00][value bytes …]
//	external (len > 2000):  [0x01][value id u32 LE][raw size u32 LE]   (9 bytes)
//
//	chunk tuple in Toast heap:  [value id u32 LE][seq u32 LE][≤1996 value bytes]
//
// Rules:
//  1. len(value) <= ToastThreshold → return a NEW slice
//     {ToastTagInline, value…}. Nothing else changes (an empty value gives
//     the 1-byte slice {0x00}).
//  2. Otherwise: id := t.NextValueID, then increment NextValueID. Cut the
//     value into consecutive chunks of ToastChunkSize bytes (the last one
//     may be shorter, never empty). For chunk seq = 0, 1, … build the
//     chunk tuple above and store it with t.Rel.Insert (Problem 4.5),
//     appending each returned TID to t.Index[id] in seq order.
//  3. Return the 9-byte external pointer. Return any Insert error as is.
//
// Example (fresh Toaster):
//
//	Toast(5000 bytes) → 01 01000000 88130000      (id 1, raw size 5000 = 0x1388)
//	  chunks 1996+1996+1008 bytes → Toast heap TIDs (0,0) (0,1) (0,2)
//	Toast(3000 bytes) → 01 02000000 b80b0000      (id 2, chunks 1996+1004 at (0,3) (0,4))
//	Toast("hi")       → 00 68 69
//	Toast(2000 bytes) → inline, 2001 bytes
//
// Constraints: one Insert per chunk.
// Postgres reference: toast_save_datum() in src/backend/access/common/toast_internals.c,
// struct varatt_external in src/include/varatt.h
func (t *Toaster) Toast(value []byte) ([]byte, error) {
	if len(value) <= ToastThreshold {
		return append([]byte{ToastTagInline}, value...), nil
	}
	id := t.NextValueID
	t.NextValueID++
	for seq, off := 0, 0; off < len(value); seq, off = seq+1, off+ToastChunkSize {
		end := min(off+ToastChunkSize, len(value))
		chunk := make([]byte, ToastChunkHeader, ToastChunkHeader+end-off)
		binary.LittleEndian.PutUint32(chunk[0:4], id)
		binary.LittleEndian.PutUint32(chunk[4:8], uint32(seq))
		chunk = append(chunk, value[off:end]...)
		tid, err := t.Rel.Insert(chunk)
		if err != nil {
			return nil, err
		}
		t.Index[id] = append(t.Index[id], tid)
	}
	ptr := make([]byte, ToastPointerSize)
	ptr[0] = ToastTagExternal
	binary.LittleEndian.PutUint32(ptr[1:5], id)
	binary.LittleEndian.PutUint32(ptr[5:9], uint32(len(value)))
	return ptr, nil
}

// Problem 4.14 — Detoast [Medium]
//
// Turn a stored value produced by Toast back into the original bytes, reassembling and validating TOAST chunks.
//
// Rules:
//  1. Empty input → ErrCorruptToast.
//  2. Tag ToastTagInline → return a copy of stored[1:] (may be empty, but
//     must be a non-nil slice).
//  3. Tag ToastTagExternal:
//     a. len(stored) must be exactly ToastPointerSize, else ErrCorruptToast.
//     b. Look up the value id in t.Index; absent → ErrCorruptToast.
//     c. For i, tid := range t.Index[id]: fetch the chunk with
//     t.Rel.Fetch (Problem 4.6). Any Fetch error, a chunk shorter than
//     ToastChunkHeader, a chunk whose value id differs from id, or whose
//     seq differs from i → ErrCorruptToast.
//     d. Concatenate the chunk payloads (bytes after the 8-byte header).
//     If the total length differs from the pointer's raw size →
//     ErrCorruptToast.
//  4. Any other tag → ErrCorruptToast.
//  5. On error return (nil, ErrCorruptToast) exactly (tests use errors.Is).
//
// Example: Detoast(Toast(v)) == v for every v; Detoast({0x02}) →
// ErrCorruptToast; a pointer to id 1 whose chunk (0,1) was deleted →
// ErrCorruptToast.
//
// Constraints: O(raw size). Do not trust the raw size for allocation
// beyond len(chunks)*ToastChunkSize — the pointer may be corrupt.
// Postgres reference: detoast_external_attr() / toast_fetch_datum() in
// src/backend/access/common/detoast.c
func (t *Toaster) Detoast(stored []byte) ([]byte, error) {
	if len(stored) == 0 {
		return nil, ErrCorruptToast
	}
	switch stored[0] {
	case ToastTagInline:
		return append([]byte{}, stored[1:]...), nil
	case ToastTagExternal:
	default:
		return nil, ErrCorruptToast
	}
	if len(stored) != ToastPointerSize {
		return nil, ErrCorruptToast
	}
	id := binary.LittleEndian.Uint32(stored[1:5])
	raw := int(binary.LittleEndian.Uint32(stored[5:9]))
	tids, ok := t.Index[id]
	if !ok {
		return nil, ErrCorruptToast
	}
	out := make([]byte, 0, min(raw, len(tids)*ToastChunkSize))
	for i, tid := range tids {
		data, _, err := t.Rel.Fetch(tid)
		if err != nil || len(data) < ToastChunkHeader ||
			binary.LittleEndian.Uint32(data[0:4]) != id ||
			binary.LittleEndian.Uint32(data[4:8]) != uint32(i) {
			return nil, ErrCorruptToast
		}
		out = append(out, data[ToastChunkHeader:]...)
	}
	if len(out) != raw {
		return nil, ErrCorruptToast
	}
	return out, nil
}
