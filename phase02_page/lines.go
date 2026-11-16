package page

// Problem 2.1 — SetItemID [Easy]
//
// Encode a line pointer and write it into an existing slot of the line-pointer array.
//
// A line pointer is 4 bytes at offset HeaderSize + 4*slot. Postgres packs three
// fields into 32 bits (lp_off:15, lp_flags:2, lp_len:15). We keep a whole
// uint16 for the offset and steal the top 2 bits of the length field for the
// state:
//
//	byte:    +0      +1      +2      +3
//	      +-------+-------+-------+-------+
//	      |  off (uint16) | flags<<14|len |
//	      +-------+-------+-------+-------+
//	                       bits 15..14 = flags, bits 13..0 = len
//
// Rules:
//  1. slot must be an existing slot: slot < NumSlots(). Otherwise return
//     ErrInvalidSlot. (This method never grows the array — callers bump lower first.)
//  2. it.Flags must be ≤ 3 and it.Len ≤ MaxItemLen (16383). Otherwise return ErrBadItemID.
//  3. On error the page is unchanged.
//  4. On success write it.Off at bytes [+0,+2) and uint16(it.Flags)<<14 | it.Len at
//     [+2,+4), both little-endian. Nothing else in the page changes; Off is
//     NOT validated against lower/upper.
//
// Example (page with lower=16, i.e. 2 slots):
//
//	p.SetItemID(1, ItemID{Off: 8185, Flags: LPNormal, Len: 2})
//	// bytes [12,16) = F9 1F 02 40   (8185 = 0x1FF9, 1<<14|2 = 0x4002)
//	p.SetItemID(0, ItemID{Off: 8187, Flags: LPDead, Len: 5})
//	// bytes [8,12)  = FB 1F 05 C0   (3<<14|5 = 0xC005)
//	p.SetItemID(2, ...)              // ErrInvalidSlot (only slots 0 and 1 exist)
//	p.SetItemID(0, ItemID{Len: 16384}) // ErrBadItemID
//
// Constraints: O(1), no allocation.
// Postgres reference: ItemIdSetNormal / ItemIdSetDead in src/include/storage/itemid.h
func (p *Page) SetItemID(slot SlotID, it ItemID) error {
	panic("TODO: Problem 2.1")
}

// Problem 2.2 — ItemID [Easy]
//
// Decode the line pointer of `slot` into an ItemID.
//
// This is the inverse of SetItemID (Problem 2.1): read the 4 bytes at
// HeaderSize + 4*slot, split the second uint16 into its top 2 bits (flags)
// and low 14 bits (length).
//
// Rules:
//  1. slot ≥ NumSlots() → return ItemID{} and ErrInvalidSlot.
//  2. Otherwise decode exactly what is stored — any state (UNUSED, NORMAL,
//     REDIRECT, DEAD) is returned as-is, no validation of off/len.
//
// Example:
//
//	// bytes [8,12) = FB 1F 05 C0, lower = 12
//	p.ItemID(0) // → ItemID{Off: 8187, Flags: LPDead, Len: 5}, nil
//	p.ItemID(1) // → ItemID{}, ErrInvalidSlot
//
// Constraints: O(1), no allocation.
// Postgres reference: PageGetItemId, ItemIdGetOffset/Flags/Length in src/include/storage/itemid.h
func (p *Page) ItemID(slot SlotID) (ItemID, error) {
	panic("TODO: Problem 2.2")
}

// Problem 2.3 — FreeSpace [Easy]
//
// Return the size of the largest tuple InsertTuple could store right now.
//
// The hole between lower and upper must hold the tuple bytes AND, unless an
// existing slot can be recycled, a new 4-byte line pointer:
//
//	+--------+------------------ hole -------------------+--------+
//	| header | lps |  (new lp?)  |   room for tuple      | tuples |
//	+--------+-----+-------------+-----------------------+--------+
//	              lower                                  upper
//
// Rules:
//  1. free = upper - lower.
//  2. If NO slot in [0, NumSlots()) is LPUnused, subtract LinePointerLen (4),
//     because inserting would need a new line pointer. (LPDead slots do NOT
//     count — a dead slot still owns its slot number until Compact.)
//  3. Never return a negative number: clamp at 0.
//
// Example:
//
//	NewPage().FreeSpace()                               // 8192-8-4   = 8180
//	lower=16 upper=8185, slots NORMAL,NORMAL            // 8169-4     = 8165
//	lower=20 upper=8186, slots NORMAL,UNUSED,NORMAL     // 8166 (slot 1 reusable)
//	lower=16 upper=18,   slots NORMAL,DEAD              // 2-4 → 0
//
// Constraints: O(NumSlots()), no allocation.
// Postgres reference: PageGetFreeSpace / PageGetHeapFreeSpace in src/backend/storage/page/bufpage.c
func (p *Page) FreeSpace() int {
	panic("TODO: Problem 2.3")
}
