package storage

import "container/list"

// FrameID indexes a frame (an 8 KB slot) of the buffer pool, 0..n-1
// (Postgres: buffer id).
type FrameID int

// Replacer decides which unpinned frame the buffer pool reuses when it needs
// room (Postgres: the clock sweep in freelist.c).
//
// The replacer TRACKS a set of frames. Each tracked frame is either
// evictable (no one has it pinned) or not. Only evictable frames may be
// chosen as victims. Frame ids are always in [0, numFrames).
//
//   - RecordAccess(f): f was just used. An untracked f becomes tracked and
//     NOT evictable. A tracked f keeps its evictable flag.
//   - SetEvictable(f, b): set f's flag. No-op if f is untracked.
//   - Evict(): choose a victim among evictable frames, STOP tracking it and
//     return (victim, true). Return (0, false) if no frame is evictable.
//   - Remove(f): stop tracking f (whatever its flag). No-op if untracked.
//   - Size(): number of tracked frames that are evictable.
//
// Replacers are not safe for concurrent use; the buffer pool's mutex protects them.
type Replacer interface {
	RecordAccess(f FrameID)
	SetEvictable(f FrameID, evictable bool)
	Evict() (FrameID, bool)
	Remove(f FrameID)
	Size() int
}

// LRUReplacer evicts the evictable frame whose last access is oldest.
//
//	order (front = least recently used)
//	front -> [f2] <-> [f0] <-> [f3] <- back (most recently used)
//	elems: f2->node, f0->node, f3->node   (O(1) lookup of a frame's node)
type LRUReplacer struct {
	order     *list.List                // tracked frames, front = LRU, back = MRU; Value is FrameID
	elems     map[FrameID]*list.Element // tracked frame -> its node in order
	evictable map[FrameID]bool          // tracked frame -> evictable flag
	size      int                       // number of evictable tracked frames
}

var (
	_ Replacer = (*LRUReplacer)(nil)
	_ Replacer = (*ClockReplacer)(nil)
	_ Replacer = (*LRUKReplacer)(nil)
)

// NewLRUReplacer returns an empty LRU replacer for numFrames frames.
func NewLRUReplacer(numFrames int) *LRUReplacer {
	return &LRUReplacer{
		order:     list.New(),
		elems:     make(map[FrameID]*list.Element, numFrames),
		evictable: make(map[FrameID]bool, numFrames),
	}
}

func (r *LRUReplacer) SetEvictable(f FrameID, evictable bool) {
	if _, ok := r.elems[f]; !ok {
		return
	}
	if r.evictable[f] != evictable {
		if evictable {
			r.size++
		} else {
			r.size--
		}
		r.evictable[f] = evictable
	}
}

func (r *LRUReplacer) Remove(f FrameID) {
	e, ok := r.elems[f]
	if !ok {
		return
	}
	if r.evictable[f] {
		r.size--
	}
	r.order.Remove(e)
	delete(r.elems, f)
	delete(r.evictable, f)
}

func (r *LRUReplacer) Size() int { return r.size }

// lruOrder lists tracked frames from least to most recently used.
func (r *LRUReplacer) lruOrder() []FrameID {
	var out []FrameID
	for e := r.order.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(FrameID))
	}
	return out
}

// Problem 3.7 — LRUReplacer.RecordAccess [Easy]
//
// Record that frame f was just used, making it the most recently used frame, in O(1).
//
// Rules:
//  1. Untracked f: append it at the BACK of r.order (most recently used),
//     store its node in r.elems and set r.evictable[f] = false.
//  2. Tracked f: move its node to the back of r.order. Its evictable flag
//     (and therefore Size()) does not change.
//
// Example:
//
//	RecordAccess(1); RecordAccess(2); RecordAccess(3)  // order [1 2 3], Size()==0
//	RecordAccess(1)                                    // order [2 3 1]
//
// Constraints: O(1) — no scanning of the list.
// Postgres reference: none — Postgres uses a clock sweep (Problem 3.9), not LRU.
func (r *LRUReplacer) RecordAccess(f FrameID) {
	panic("TODO: Problem 3.7")
}

// Problem 3.8 — LRUReplacer.Evict [Medium]
//
// Remove and return the least recently used evictable frame.
//
// Rules:
//  1. Walk r.order from the front (least recently used); the first frame
//     whose flag is evictable is the victim. Non-evictable (pinned) frames
//     are skipped and keep their position.
//  2. Stop tracking the victim (remove it from order, elems, evictable) and
//     decrement size. A later SetEvictable on it is a no-op until it is
//     accessed again.
//  3. No evictable frame (including an empty replacer) -> (0, false).
//
// Example:
//
//	RecordAccess 1,2,3,4 ; SetEvictable(f,true) for all ; RecordAccess(1)
//	Evict() -> 2, then 3, then 4, then 1, then (0,false)
//
// Constraints: O(1) when the front frame is evictable; O(number of pinned frames skipped) otherwise.
func (r *LRUReplacer) Evict() (FrameID, bool) {
	panic("TODO: Problem 3.8")
}
