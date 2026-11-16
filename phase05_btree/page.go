package btree

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 5.20 — EncodePage [Medium]
//
// Serialize a node into one 8192-byte slotted page: header, line pointers, and index tuples.
//
// All integers are little-endian.
//
//	0      2       4      6      8      12     16
//	+------+-------+------+------+------+------+------+------+-- free --+---------+---------+
//	|flags |nitems |lower |upper | prev | next | lp 0 | lp 1 |          | tuple 1 | tuple 0 |
//	+------+-------+------+------+------+------+------+------+----------+---------+---------+
//	                                            line pointers ->      <- tuples       8192
//
//	flags  uint16  1 for a leaf, 0 for an internal node
//	nitems uint16  number of line pointers / tuples
//	lower  uint16  16 + 4*nitems   (end of the line-pointer array)
//	upper  uint16  start of the lowest tuple (8192 when there are none)
//	prev   uint32  n.Prev            next  uint32  n.Next
//	line pointer i (4 bytes at 16+4*i): offset uint16 | length uint16 of tuple i
//
//	leaf tuple:     keyLen u16 | key | tid.Page u32 | tid.Slot u16 | payloadLen u16 | payload
//	internal tuple: keyLen u16 | key | tid.Page u32 | tid.Slot u16 | child u32
//
// Rules:
//  1. A leaf with k items has k tuples, one per item, in order.
//  2. An internal node with k separators has k+1 tuples. Tuple 0 is the
//     "minus infinity" pivot: keyLen 0, TID (0,0), child = Children[0].
//     Tuple i (i >= 1) holds Items[i-1].Key, Items[i-1].TID and Children[i].
//  3. Tuples are placed from the end of the page downward in order: tuple 0
//     occupies [8192-len0, 8192), tuple 1 sits directly below it, and so on.
//  4. Every byte not covered by the header, line pointers or tuples is 0.
//  5. If 16 + 4*nitems + total tuple bytes > 8192 -> (nil, ErrNodeTooLarge).
//     n.ID is not stored (the page's position is its ID).
//
// Example (leaf, Prev 7, Next Invalid, items ("ab",(1,2)) and ("c",(3,4),"xy")):
//
//	header: 01 00 | 02 00 | 18 00 | e7 1f | 07 00 00 00 | ff ff ff ff     lower 24, upper 8167
//	lp 0 = (8180, 12)   [8180,8192): 02 00 61 62 01 00 00 00 02 00 00 00
//	lp 1 = (8167, 13)   [8167,8180): 01 00 63 03 00 00 00 04 00 02 00 78 79
//
// Constraints: one 8192-byte allocation.
// Postgres reference: BTPageOpaqueData + _bt_pgaddtup() in src/backend/access/nbtree/nbtinsert.c,
// "minus infinity" item: P_FIRSTDATAKEY in src/include/access/nbtree.h
func (n *Node) EncodePage() ([]byte, error) {
	panic("TODO: Problem 5.20")
}

// Problem 5.21 — DecodePage [Medium]
//
// Parse a page written by EncodePage back into a Node, rejecting anything malformed.
//
// Rules (layout: see EncodePage):
//  1. The returned node has ID = id, Leaf from flags, Prev/Next from the
//     header. Leaf: Items from the tuples, Children nil. Internal: Children
//     from every tuple's child; Items from tuples 1..n-1 (tuple 0's key and
//     TID are ignored).
//  2. Copy keys and payloads out of buf (the node must not alias buf).
//     Keys are never nil (an empty key decodes as []byte{}); a payload of
//     length 0 decodes as nil. Separator Payloads are nil.
//  3. Return (nil, ErrCorruptPage) when:
//     - len(buf) != 8192
//     - flags is not 0 or 1
//     - lower != 16 + 4*nitems, or upper < lower, or upper > 8192
//     - a line pointer's range [offset, offset+length) is not inside [upper, 8192)
//     - a tuple's length does not match exactly what its own keyLen
//     (and payloadLen, for a leaf) say it should be
//     - an internal page has 0 tuples
//
// Example:
//
//	DecodePage(3, leafExampleBytes) -> &Node{ID: 3, Leaf: true, Prev: 7, Next: Invalid,
//	                                   Items: [("ab",(1,2)), ("c",(3,4),"xy")]}
//
// Constraints: O(page size); never panics on garbage input.
// Postgres reference: PageGetItem() / _bt_checkpage() in src/backend/access/nbtree/nbtpage.c
func DecodePage(id types.PageID, buf []byte) (*Node, error) {
	panic("TODO: Problem 5.21")
}
