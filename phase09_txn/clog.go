// Package txn models PostgreSQL's transaction machinery in memory: the commit
// log (clog), transaction ids and their wraparound arithmetic, MVCC snapshots,
// tuple visibility, isolation levels, write-write conflicts, savepoints, the
// heavyweight lock manager with deadlock detection, VACUUM and freezing.
package txn

import (
	"errors"
	"fmt"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Special transaction ids (Postgres: src/include/access/transam.h).
const (
	// BootstrapTxnID is the xid used while running initdb. It counts as committed.
	BootstrapTxnID types.TxnID = 1
	// FrozenTxnID is the xid of tuples frozen by old-style VACUUM FREEZE. It counts as
	// committed and is older than every normal xid.
	FrozenTxnID types.TxnID = 2
	// FirstNormalTxnID is the first xid ever handed out to a real transaction.
	FirstNormalTxnID types.TxnID = 3
)

// IsNormal reports whether xid is a normal (>= 3) transaction id.
func IsNormal(xid types.TxnID) bool { return xid >= FirstNormalTxnID }

// TxnStatus is the 2-bit state of one transaction stored in the clog
// (Postgres: XidStatus, TRANSACTION_STATUS_*).
type TxnStatus uint8

const (
	StatusInProgress   TxnStatus = 0 // 0b00 — running, or never heard of
	StatusCommitted    TxnStatus = 1 // 0b01
	StatusAborted      TxnStatus = 2 // 0b10
	StatusSubCommitted TxnStatus = 3 // 0b11 — subtransaction committed, parent still running
)

func (s TxnStatus) String() string {
	switch s {
	case StatusInProgress:
		return "in-progress"
	case StatusCommitted:
		return "committed"
	case StatusAborted:
		return "aborted"
	case StatusSubCommitted:
		return "sub-committed"
	}
	return fmt.Sprintf("status(%d)", uint8(s))
}

// Clog geometry (Postgres: src/backend/access/transam/clog.c).
const (
	ClogPageSize     = 8192                            // bytes per clog page (BLCKSZ)
	ClogBitsPerXact  = 2                               // CLOG_BITS_PER_XACT
	ClogXactsPerByte = 4                               // CLOG_XACTS_PER_BYTE
	ClogXactsPerPage = ClogPageSize * ClogXactsPerByte // CLOG_XACTS_PER_PAGE = 32768
)

var (
	// ErrBadStatus is returned by Clog.Set for a status value that does not fit in 2 bits.
	ErrBadStatus = errors.New("clog: invalid transaction status")
	// ErrSpecialXid is returned by Clog.Set for xids 0, 1 and 2, which are never stored.
	ErrSpecialXid = errors.New("clog: cannot set status of a special xid")
)

// Clog is the commit log: 2 bits of status per xid, packed 4 xids per byte,
// 32768 xids per 8 KB page. Pages are created lazily, zero-filled
// (zero = StatusInProgress).
type Clog struct {
	// Pages maps a clog page number to its ClogPageSize-byte buffer.
	Pages map[uint32][]byte
}

// NewClog returns an empty commit log.
func NewClog() *Clog { return &Clog{Pages: map[uint32][]byte{}} }

// Page returns clog page n, or nil if it was never written.
func (c *Clog) Page(n uint32) []byte { return c.Pages[n] }

// Problem 9.1 — ClogSet [Easy]
//
// Store the 2-bit status of xid in the packed commit log.
//
// Every transaction's fate lives in pg_xact (the "clog"): 2 bits per xid, so one
// 8 KB page holds 32768 xids. The xid → position mapping is pure arithmetic:
//
//	page  = xid / 32768                (ClogXactsPerPage)
//	byte  = (xid % 32768) / 4          (index inside that page)
//	shift = (xid % 4) * 2              (bit offset inside that byte)
//
//	byte k of a page:   bit 7 6 | 5 4 | 3 2 | 1 0
//	                    xid 4k+3 |4k+2 |4k+1 | 4k
//
// Rules:
//  1. status > 3 → ErrBadStatus. xid < FirstNormalTxnID (0, 1, 2) → ErrSpecialXid.
//     On error the clog is unchanged (no page is created).
//  2. If the page does not exist yet, create it as ClogPageSize zero bytes.
//  3. Replace exactly the 2 bits of xid (clear the old value first); the other
//     3 xids sharing that byte must keep their bits.
//
// Example:
//
//	c := NewClog()
//	c.Set(3, StatusCommitted)   // page 0, byte 0 = 0x40  (01 at bits 6-7)
//	c.Set(4, StatusAborted)     // page 0, byte 1 = 0x02
//	c.Set(5, StatusSubCommitted) // page 0, byte 1 = 0x0E
//	c.Set(7, StatusCommitted)   // page 0, byte 1 = 0x4E
//	c.Set(4, StatusCommitted)   // page 0, byte 1 = 0x4D  (old 10 replaced by 01)
//	c.Set(32768, StatusAborted) // page 1, byte 0 = 0x02
//
// Constraints: O(1); allocate at most one page.
// Postgres reference: TransactionIdSetStatusBit() in src/backend/access/transam/clog.c
func (c *Clog) Set(xid types.TxnID, status TxnStatus) error {
	panic("TODO: Problem 9.1")
}

// Problem 9.2 — ClogGet [Easy]
//
// Read back the status of xid from the packed commit log.
//
// Uses the same page / byte / shift arithmetic as ClogSet (Problem 9.1).
//
// Rules:
//  1. InvalidTxnID (0) → StatusAborted (it never committed anything).
//  2. BootstrapTxnID (1) and FrozenTxnID (2) → StatusCommitted.
//  3. A normal xid whose page does not exist → StatusInProgress (pages are zero-filled).
//  4. Otherwise return the 2 bits stored for xid.
//
// Example:
//
//	c := NewClog()
//	c.Get(10)                     // StatusInProgress (no page yet)
//	c.Set(10, StatusAborted)
//	c.Get(10)                     // StatusAborted
//	c.Get(9), c.Get(11)           // StatusInProgress (neighbours untouched)
//	c.Get(0), c.Get(2)            // StatusAborted, StatusCommitted
//
// Constraints: O(1), no allocation.
// Postgres reference: TransactionIdGetStatus() in clog.c, TransactionLogFetch() in transam.c
func (c *Clog) Get(xid types.TxnID) TxnStatus {
	panic("TODO: Problem 9.2")
}

// Problem 9.3 — TxnIDPrecedes [Easy]
//
// Compare two xids the way Postgres does: modulo 2^32, so the counter can wrap around.
//
// Xids are 32-bit and the counter wraps (…, 4294967294, 4294967295, 3, 4, …).
// Postgres therefore treats the xid space as a circle: for every normal xid,
// the 2^31 xids "behind" it are in the past and the 2^31 "ahead" are in the
// future. a precedes b iff the signed 32-bit difference a-b is negative.
//
//	        past  ◄──── b ────►  future
//	b-2^31 ........... b ........... b+2^31-1     (mod 2^32)
//
// Rules:
//  1. If a or b is not normal (0, 1, 2), compare them as plain unsigned
//     integers: a < b. (Special xids are older than everything.)
//  2. Otherwise return int32(a-b) < 0 (uint32 subtraction, then reinterpret as signed).
//  3. An xid never precedes itself.
//
// Example:
//
//	TxnIDPrecedes(100, 200)              // true
//	TxnIDPrecedes(4294967290, 5)         // true  (5 is "after" the wrap)
//	TxnIDPrecedes(5, 4294967290)         // false
//	TxnIDPrecedes(100, 2147483747)       // true  (2^31-1 ahead: still the future)
//	TxnIDPrecedes(100, 2147483749)       // false (2^31+1 ahead: wrapped, now the past)
//	TxnIDPrecedes(2147483749, 100)       // true
//	TxnIDPrecedes(100, 2147483748)       // true  (exactly 2^31 apart: int32(-2^31) < 0,
//	TxnIDPrecedes(2147483748, 100)       // true   so each "precedes" the other)
//	TxnIDPrecedes(2, 4294967290)         // true  (special xid → plain compare)
//	TxnIDPrecedes(4294967290, 2)         // false
//
// Constraints: O(1).
// Postgres reference: TransactionIdPrecedes() in src/backend/access/transam/transam.c
func TxnIDPrecedes(a, b types.TxnID) bool {
	panic("TODO: Problem 9.3")
}
