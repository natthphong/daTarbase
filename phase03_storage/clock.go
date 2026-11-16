package storage

// MaxUsageCount caps a frame's usage count (Postgres BM_MAX_USAGE_COUNT).
const MaxUsageCount = 5

// ClockReplacer is Postgres's clock-sweep ("second chance with counts").
// Frames sit on a circle; the hand walks it looking for an evictable frame
// whose usage count has dropped to 0, decrementing counts as it passes.
//
//	         hand
//	          v
//	[f0 u=0] [f1 u=2] [f2 u=1 pinned] [f3 u=0] ...   (wraps around)
type ClockReplacer struct {
	usage     []int  // usage count per frame, 0..MaxUsageCount
	tracked   []bool // tracked[f]: frame f is tracked
	evictable []bool // evictable[f]: tracked frame f may be evicted
	hand      int    // next frame the sweep looks at (Postgres nextVictimBuffer)
	size      int    // number of evictable tracked frames
}

// NewClockReplacer returns an empty clock replacer for numFrames frames; the hand starts at frame 0.
func NewClockReplacer(numFrames int) *ClockReplacer {
	return &ClockReplacer{
		usage:     make([]int, numFrames),
		tracked:   make([]bool, numFrames),
		evictable: make([]bool, numFrames),
	}
}

// RecordAccess starts tracking f (not evictable, usage 0) if needed, then
// adds 1 to its usage count, capped at MaxUsageCount (Postgres PinBuffer).
func (r *ClockReplacer) RecordAccess(f FrameID) {
	if !r.tracked[f] {
		r.tracked[f], r.evictable[f], r.usage[f] = true, false, 0
	}
	if r.usage[f] < MaxUsageCount {
		r.usage[f]++
	}
}

func (r *ClockReplacer) SetEvictable(f FrameID, evictable bool) {
	if !r.tracked[f] || r.evictable[f] == evictable {
		return
	}
	r.evictable[f] = evictable
	if evictable {
		r.size++
	} else {
		r.size--
	}
}

func (r *ClockReplacer) Remove(f FrameID) {
	if !r.tracked[f] {
		return
	}
	if r.evictable[f] {
		r.size--
	}
	r.tracked[f], r.evictable[f], r.usage[f] = false, false, 0
}

func (r *ClockReplacer) Size() int { return r.size }

// Problem 3.9 — ClockReplacer.Evict [Medium]
//
// Run Postgres's clock sweep: advance the hand, decrementing usage counts, until an evictable frame with usage 0 is found.
//
// Each step looks at frame r.hand, then moves the hand to the next frame
// ((hand+1) % numFrames, wrapping around):
//
//  1. Untracked or non-evictable (pinned) frame: skip it. Its usage count
//     is NOT decremented (Postgres only decrements unpinned buffers).
//  2. Evictable frame with usage > 0: decrement its usage by 1 and move on.
//  3. Evictable frame with usage == 0: this is the victim. Stop tracking
//     it (usage 0, not evictable, untracked), decrement size, leave the
//     hand pointing just PAST the victim, and return (victim, true).
//  4. If Size() == 0, return (0, false) immediately without moving the
//     hand (otherwise the sweep would never end).
//
// Example (4 frames, hand at 0):
//
//	RecordAccess 0,1,2,3 then RecordAccess(1)   usage = [1 2 1 1], all evictable
//	Evict() -> 0   (sweep: f0 1->0, f1 2->1, f2 1->0, f3 1->0, f0 is 0 -> victim; hand=1)
//	Evict() -> 2   (f1 1->0, f2 is 0 -> victim; hand=3)
//	Evict() -> 3   (hand=0)
//	Evict() -> 1   (f0 untracked: skipped; hand=2)
//	Evict() -> (0, false)
//
// Constraints: at most (MaxUsageCount+1) full turns of the hand per call.
// Postgres reference: StrategyGetBuffer() + ClockSweepTick() in src/backend/storage/buffer/freelist.c
func (r *ClockReplacer) Evict() (FrameID, bool) {
	if r.size == 0 {
		return 0, false
	}
	n := len(r.usage)
	for {
		f := FrameID(r.hand)
		r.hand = (r.hand + 1) % n
		if !r.tracked[f] || !r.evictable[f] {
			continue
		}
		if r.usage[f] > 0 {
			r.usage[f]--
			continue
		}
		r.tracked[f], r.evictable[f] = false, false
		r.size--
		return f, true
	}
}
