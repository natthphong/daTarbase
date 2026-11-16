package txn

import (
	"errors"
	"slices"

	"github.com/natthphong/daTarbase/pkg/types"
)

// LockMode is a heavyweight lock mode (Postgres: LOCKMODE, lockdefs.h).
// Table statements take: SELECT → AccessShare, INSERT/UPDATE/DELETE → RowExclusive,
// CREATE INDEX → Share, DROP TABLE / TRUNCATE → AccessExclusive, ….
// For row-level tags this phase uses ShareLock (≈ SELECT … FOR SHARE) and
// ExclusiveLock (≈ SELECT … FOR UPDATE).
type LockMode uint8

const (
	AccessShareLock          LockMode = iota + 1 // 1
	RowShareLock                                 // 2
	RowExclusiveLock                             // 3
	ShareUpdateExclusiveLock                     // 4
	ShareLock                                    // 5
	ShareRowExclusiveLock                        // 6
	ExclusiveLock                                // 7
	AccessExclusiveLock                          // 8
)

// LockTag names a lockable object, e.g. "rel:accounts" or "row:accounts(0,1)".
type LockTag string

// LockRequest is one transaction's request for (or grant of) a mode.
type LockRequest struct {
	Xid  types.TxnID
	Mode LockMode
}

// LockState is the lock table entry for one tag (Postgres: LOCK + PROCLOCKs).
type LockState struct {
	Granted []LockRequest // granted locks, in grant order
	Waiting []LockRequest // FIFO wait queue (oldest first)
}

var (
	// ErrBadLockMode: mode outside 1..8.
	ErrBadLockMode = errors.New("lock: invalid lock mode")
	// ErrAlreadyWaiting: a transaction that is already queued on some lock is
	// asleep and cannot issue another request.
	ErrAlreadyWaiting = errors.New("lock: transaction is already waiting for a lock")
)

// LockManager is the heavyweight lock table.
type LockManager struct {
	// Locks holds one entry per tag that has granted or waiting requests.
	// Entries with no granted and no waiting requests are deleted.
	Locks map[LockTag]*LockState
}

// NewLockManager returns an empty lock table.
func NewLockManager() *LockManager { return &LockManager{Locks: map[LockTag]*LockState{}} }

// Holders returns a copy of the granted requests on tag.
func (lm *LockManager) Holders(tag LockTag) []LockRequest {
	if s := lm.Locks[tag]; s != nil {
		return append([]LockRequest(nil), s.Granted...)
	}
	return nil
}

// Waiters returns a copy of the wait queue of tag.
func (lm *LockManager) Waiters(tag LockTag) []LockRequest {
	if s := lm.Locks[tag]; s != nil {
		return append([]LockRequest(nil), s.Waiting...)
	}
	return nil
}

// Problem 9.16 — LockConflicts [Easy]
//
// Report whether lock modes a and b conflict (cannot be held at once by two different transactions).
//
// Postgres' table-lock conflict matrix (X = conflict). It is symmetric.
//
//	                             held:  1 2 3 4 5 6 7 8
//	requested 1 AccessShare             . . . . . . . X
//	          2 RowShare                . . . . . . X X
//	          3 RowExclusive            . . . . X X X X
//	          4 ShareUpdateExclusive    . . . X X X X X
//	          5 Share                   . . X X . X X X
//	          6 ShareRowExclusive       . . X X X X X X
//	          7 Exclusive               . X X X X X X X
//	          8 AccessExclusive         X X X X X X X X
//
// So SELECT (AS) only conflicts with AX (DROP/TRUNCATE); concurrent writers (RX)
// don't block each other at table level (rows sort that out); and Share vs
// Share is fine but Share vs RowExclusive is not (CREATE INDEX blocks writes).
//
// Rules:
//  1. Return true iff the cell (a, b) is X in the table above.
//  2. Both a and b are valid modes (1..8); callers check that.
//
// Example:
//
//	LockConflicts(AccessShareLock, AccessExclusiveLock)  // true
//	LockConflicts(RowExclusiveLock, RowExclusiveLock)    // false
//	LockConflicts(ShareLock, RowExclusiveLock)           // true
//	LockConflicts(ShareLock, ShareLock)                  // false
//
// Constraints: O(1). Hint: Postgres stores one bitmask of conflicting modes per mode.
// Postgres reference: LockConflicts[] in src/backend/storage/lmgr/lock.c
func LockConflicts(a, b LockMode) bool {
	bit := func(ms ...LockMode) uint16 {
		var m uint16
		for _, x := range ms {
			m |= 1 << x
		}
		return m
	}
	table := [9]uint16{
		AccessShareLock:          bit(AccessExclusiveLock),
		RowShareLock:             bit(ExclusiveLock, AccessExclusiveLock),
		RowExclusiveLock:         bit(ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock),
		ShareUpdateExclusiveLock: bit(ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock),
		ShareLock:                bit(RowExclusiveLock, ShareUpdateExclusiveLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock),
		ShareRowExclusiveLock:    bit(RowExclusiveLock, ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock),
		ExclusiveLock:            bit(RowShareLock, RowExclusiveLock, ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock),
		AccessExclusiveLock:      bit(AccessShareLock, RowShareLock, RowExclusiveLock, ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock),
	}
	return table[a]&(1<<b) != 0
}

// Problem 9.17 — Acquire [Medium]
//
// Request lock mode on tag for transaction xid: grant it now or append it to the FIFO wait queue.
//
// Rules, checked in this order:
//  1. mode outside 1..8 → (false, ErrBadLockMode).
//  2. xid already has a waiting request on ANY tag → (false, ErrAlreadyWaiting).
//  3. xid already holds exactly this mode on tag → (true, nil), nothing changes
//     (re-acquiring is free; no reference counting).
//  4. Grant immediately iff mode conflicts (LockConflicts, Problem 9.16) with
//     NO granted lock held by another xid on tag AND with NO request in tag's
//     wait queue. (Checking the queue keeps the order fair: a stream of
//     AccessShare requests must not starve a queued AccessExclusive.)
//     Locks held by xid itself never block it.
//  5. Granted → append {xid, mode} to Granted, return (true, nil).
//     Otherwise append {xid, mode} to Waiting, return (false, nil).
//  6. Create tag's LockState on first use.
//
// Example ("rel:t"):
//
//	Acquire(10, "rel:t", AccessShareLock)      // true
//	Acquire(11, "rel:t", AccessExclusiveLock)  // false → Waiting [11 AX]
//	Acquire(12, "rel:t", AccessShareLock)      // false → Waiting [11 AX, 12 AS]
//	                                           //   (no conflict with holder 10, but conflicts with queued 11)
//	Acquire(11, "rel:u", AccessShareLock)      // false, ErrAlreadyWaiting (11 is asleep)
//	Acquire(10, "rel:t", AccessShareLock)      // true (already held, nothing changes)
//
// Constraints: O(granted + waiting) per call, plus the scan for rule 2.
// Postgres reference: LockAcquireExtended() / LockCheckConflicts() in src/backend/storage/lmgr/lock.c
func (lm *LockManager) Acquire(xid types.TxnID, tag LockTag, mode LockMode) (bool, error) {
	if mode < AccessShareLock || mode > AccessExclusiveLock {
		return false, ErrBadLockMode
	}
	for _, s := range lm.Locks {
		for _, w := range s.Waiting {
			if w.Xid == xid {
				return false, ErrAlreadyWaiting
			}
		}
	}
	s := lm.Locks[tag]
	if s == nil {
		s = &LockState{}
		lm.Locks[tag] = s
	}
	req := LockRequest{Xid: xid, Mode: mode}
	blocked := false
	for _, g := range s.Granted {
		if g == req {
			return true, nil
		}
		if g.Xid != xid && LockConflicts(g.Mode, mode) {
			blocked = true
		}
	}
	for _, w := range s.Waiting {
		if LockConflicts(w.Mode, mode) {
			blocked = true
		}
	}
	if blocked {
		s.Waiting = append(s.Waiting, req)
		return false, nil
	}
	s.Granted = append(s.Granted, req)
	return true, nil
}

// Problem 9.18 — Release [Hard]
//
// Drop everything xid has on tag (granted modes and a queued request) and wake up the waiters that can now run.
//
// Rules:
//  1. Remove every granted entry of xid on tag and xid's waiting request on tag
//     (if any). Unknown tag or nothing held → return nil, nothing changes.
//  2. Wake-up pass over the wait queue in FIFO order, keeping a set "ahead" of
//     modes requested by waiters that stay asleep (initially empty). For each waiter W:
//     - if W.Mode conflicts with no granted lock of another xid AND with no
//     mode in "ahead": move W to the end of Granted and to the result;
//     - otherwise W stays in the queue (relative order preserved) and W.Mode
//     is added to "ahead".
//     A waiter granted earlier in the pass counts as granted for later waiters.
//  3. If the tag ends up with no granted and no waiting requests, delete it from lm.Locks.
//  4. Return the newly granted requests in grant order (nil if none).
//
// Example ("rel:t": Granted [10 AS], Waiting [11 AX, 12 AS, 13 AS]):
//
//	Release(10, "rel:t") → [{11 AX}]          Granted [11 AX], Waiting [12 AS, 13 AS]
//	Release(11, "rel:t") → [{12 AS} {13 AS}]  both shared waiters wake together
//
// Constraints: O(granted × waiting).
// Postgres reference: LockRelease() / ProcLockWakeup() in src/backend/storage/lmgr/proc.c
func (lm *LockManager) Release(xid types.TxnID, tag LockTag) []LockRequest {
	s := lm.Locks[tag]
	if s == nil {
		return nil
	}
	without := func(rs []LockRequest) []LockRequest {
		out := rs[:0]
		for _, r := range rs {
			if r.Xid != xid {
				out = append(out, r)
			}
		}
		return out
	}
	s.Granted, s.Waiting = without(s.Granted), without(s.Waiting)

	var woken []LockRequest
	var ahead []LockMode
	still := s.Waiting[:0]
	for _, w := range s.Waiting {
		ok := true
		for _, g := range s.Granted {
			if g.Xid != w.Xid && LockConflicts(g.Mode, w.Mode) {
				ok = false
			}
		}
		for _, m := range ahead {
			if LockConflicts(m, w.Mode) {
				ok = false
			}
		}
		if ok {
			s.Granted = append(s.Granted, w)
			woken = append(woken, w)
		} else {
			still = append(still, w)
			ahead = append(ahead, w.Mode)
		}
	}
	s.Waiting = still
	if len(s.Granted) == 0 && len(s.Waiting) == 0 {
		delete(lm.Locks, tag)
	}
	return woken
}

// Problem 9.19 — DetectDeadlock [Medium]
//
// Build the wait-for graph of the lock table, find every transaction stuck on a cycle, and pick the youngest as the victim.
//
// The wait-for graph has an edge W → H ("W waits for H") for every waiting
// request W on a tag and every other xid H (H != W.Xid) such that, on the same tag,
//   - H holds a granted mode that conflicts with W.Mode ("hard" edge), or
//   - H has a waiting request EARLIER in the queue whose mode conflicts with
//     W.Mode ("soft" edge — W is queued behind H and FIFO order will make it wait).
//
// A transaction is deadlocked iff it lies on a cycle of this graph (it can reach
// itself by following edges). Postgres aborts one of them; this phase picks the
// youngest, i.e. the largest xid (plain numeric comparison, ignore wraparound),
// because it has probably done the least work.
//
// Rules:
//  1. Return all deadlocked xids sorted ascending, and the victim = the largest of them.
//  2. No cycle → (nil, InvalidTxnID).
//  3. If there are several independent cycles, all their members are reported
//     and the victim is the largest xid overall.
//
// Example:
//
//	T10 holds "row:1" X, T11 holds "row:2" X
//	T10 waits for "row:2" X, T11 waits for "row:1" X   → ([10 11], 11)
//
//	lock upgrade: T10, T11 both hold "rel:t" AS; T10 waits for AX, T11 waits for AX
//	→ T10 → T11 (granted AS), T11 → T10 (granted AS and queued AX) → ([10 11], 11)
//
// Constraints: O(V·(V+E)) is fine.
// Postgres reference: DeadLockCheck() / FindLockCycle() in src/backend/storage/lmgr/deadlock.c
func (lm *LockManager) DetectDeadlock() ([]types.TxnID, types.TxnID) {
	edges := map[types.TxnID][]types.TxnID{}
	for _, s := range lm.Locks {
		for i, w := range s.Waiting {
			for _, g := range s.Granted {
				if g.Xid != w.Xid && LockConflicts(g.Mode, w.Mode) {
					edges[w.Xid] = append(edges[w.Xid], g.Xid)
				}
			}
			for _, e := range s.Waiting[:i] {
				if e.Xid != w.Xid && LockConflicts(e.Mode, w.Mode) {
					edges[w.Xid] = append(edges[w.Xid], e.Xid)
				}
			}
		}
	}
	reachesSelf := func(start types.TxnID) bool {
		seen := map[types.TxnID]bool{}
		stack := append([]types.TxnID(nil), edges[start]...)
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if x == start {
				return true
			}
			if seen[x] {
				continue
			}
			seen[x] = true
			stack = append(stack, edges[x]...)
		}
		return false
	}
	var dead []types.TxnID
	for x := range edges {
		if reachesSelf(x) {
			dead = append(dead, x)
		}
	}
	if len(dead) == 0 {
		return nil, types.InvalidTxnID
	}
	slices.Sort(dead)
	return dead, dead[len(dead)-1]
}
