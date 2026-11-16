package page

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 2.11 — ComputeChecksum [Medium]
//
// Compute the 16-bit checksum of the page as it would be stored on disk as block `id`.
//
// Postgres (initdb --data-checksums) stamps every page with a 16-bit checksum
// right before it is written and verifies it right after it is read, to catch
// torn writes and bit rot. The block number is mixed in so a page that is
// intact but was written to the WRONG place in the file is detected too.
// Postgres uses a vectorised FNV-1a variant; we use plain FNV-1a 32:
//
//	h := 2166136261                    // FNV offset basis
//	for each byte b of the page:       // all 8192 bytes, index 0 first
//	    h ^= uint32(b)
//	    h *= 16777619                  // FNV prime, uint32 wrap-around
//
// Rules:
//  1. Hash all PageSize bytes in order, but treat the checksum field (bytes
//     6 and 7) as if they were zero — the stored checksum must not feed into
//     its own computation. The page itself must NOT be modified.
//  2. Mix in the block number: h ^= uint32(id).
//  3. Fold to 16 bits exactly like Postgres: return uint16(h % 65535) + 1.
//     The result is therefore never 0 (Postgres reserves 0 for "no checksum").
//
// Example:
//
//	NewPage().ComputeChecksum(0)  // → 16637
//	NewPage().ComputeChecksum(1)  // → 16636
//	// page after InsertTuple("hello"), InsertTuple("db"):
//	p.ComputeChecksum(7)          // → 9245, whatever value bytes 6..8 hold
//
// Constraints: O(PageSize), no allocation (hash/fnv is allowed but needs care with rule 1).
// Postgres reference: pg_checksum_page() in src/include/storage/checksum_impl.h
func (p *Page) ComputeChecksum(id types.PageID) uint16 {
	panic("TODO: Problem 2.11")
}

// Problem 2.12 — VerifyChecksum [Easy]
//
// Check that the stored checksum matches the page contents for block `id`.
//
// This is what the buffer manager runs on every page it reads from disk.
//
// Rules:
//  1. Return nil if StoredChecksum() == ComputeChecksum(id) (Problem 2.11).
//  2. Otherwise return an error that wraps ErrChecksumMismatch (errors.Is must
//     succeed), e.g. fmt.Errorf("%w: block 3: stored 1, computed 2", ErrChecksumMismatch).
//  3. A stored value of 0 is never valid (ComputeChecksum never returns 0), so
//     a page that was never stamped fails verification.
//  4. The page is not modified.
//
// Example:
//
//	p := NewPage()
//	p.VerifyChecksum(0)                        // ErrChecksumMismatch (stored 0)
//	p.SetStoredChecksum(p.ComputeChecksum(0))
//	p.VerifyChecksum(0)                        // nil
//	p.VerifyChecksum(1)                        // ErrChecksumMismatch (page belongs to block 0)
//
// Constraints: O(PageSize).
// Postgres reference: PageIsVerifiedExtended() in src/backend/storage/page/bufpage.c
func (p *Page) VerifyChecksum(id types.PageID) error {
	panic("TODO: Problem 2.12")
}

// Problem 2.13 — Serialize [Easy]
//
// Return a private copy of the page image, with the checksum for block `id` stamped into the copy.
//
// Postgres may set hint bits on a page while it holds only a SHARED lock, so
// the page can change while it is being written out. If it stamped the
// checksum on the shared page and then wrote it, a concurrent change in
// between would make the on-disk checksum wrong. PageSetChecksumCopy therefore
// copies the page first and stamps only the copy.
//
// Rules:
//  1. Return a new []byte of length PageSize that does not alias the page.
//  2. The copy equals the page, except bytes 6..8 hold ComputeChecksum(id)
//     (Problem 2.11), little-endian.
//  3. The page itself is not modified (its StoredChecksum() stays whatever it was).
//
// Example:
//
//	p := NewPage()
//	b := p.Serialize(0)   // b[6:8] = FD 40 (16637 = 0x40FD); p.StoredChecksum() still 0
//
// Constraints: exactly one PageSize allocation.
// Postgres reference: PageSetChecksumCopy() in src/backend/storage/page/bufpage.c
func (p *Page) Serialize(id types.PageID) []byte {
	panic("TODO: Problem 2.13")
}

// Problem 2.14 — Load [Medium]
//
// Build a Page from an on-disk image of block `id`, rejecting torn or corrupt pages.
//
// Never trust bytes from disk: a buggy lower/upper would make every later
// method read or write outside the page. Postgres validates the header of every
// page it reads (PageIsVerifiedExtended) and the line pointers before it moves
// tuples (PageRepairFragmentation's "corrupted line pointer" errors).
//
// Rules (checked in this order; the first failure wins):
//  1. len(b) != PageSize → ErrCorruptPage.
//  2. If every byte of b is zero, the block was allocated but never written
//     (e.g. the file was extended and we crashed). That is NOT corruption:
//     return NewPage(), nil.
//  3. Checksum: the stored checksum (bytes 6..8) must equal the checksum of b
//     for block id (Problem 2.11) → else an error wrapping ErrChecksumMismatch.
//  4. Header sanity, else ErrCorruptPage:
//     HeaderSize ≤ lower ≤ upper ≤ PageSize, (lower-HeaderSize) % 4 == 0, and
//     flags == 0 (no flag bits are defined in this phase).
//  5. Every line pointer, else ErrCorruptPage:
//     - LPUnused  → off == 0 and len == 0;
//     - LPNormal or LPDead → len ≥ 1, off ≥ upper, off+len ≤ PageSize;
//     - LPRedirect → corrupt (not used in this phase).
//  6. On success return a new Page holding a COPY of b: later changes to b must
//     not affect the page.
//
// Example:
//
//	b := p.Serialize(5)
//	q, err := Load(b, 5)          // q.Bytes() == b, err nil
//	Load(b, 6)                    // ErrChecksumMismatch (wrong block)
//	Load(make([]byte, 8192), 9)   // NewPage(), nil
//	Load(make([]byte, 100), 0)    // ErrCorruptPage
//	// image with lower=10 and a valid checksum → ErrCorruptPage (misaligned)
//
// Constraints: O(PageSize).
// Postgres reference: PageIsVerifiedExtended() and PageHeaderIsValid in src/backend/storage/page/bufpage.c
func Load(b []byte, id types.PageID) (*Page, error) {
	panic("TODO: Problem 2.14")
}
