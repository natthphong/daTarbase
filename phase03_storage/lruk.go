package storage

// LRUKReplacer implements LRU-K (O'Neil, O'Neil & Weikum, 1993).
//
// Every RecordAccess gets a timestamp from a logical clock (0, 1, 2, ...).
// For each tracked frame the replacer keeps its last (up to) K access
// timestamps, oldest first:
//
//	K = 2, accesses: f0@0 f1@1 f2@2 f0@3 f1@4
//	history: f0 = [0 3]   f1 = [1 4]   f2 = [2]
//
// The backward K-distance of a frame is now - (timestamp of its K-th most
// recent access) — i.e. now - history[0] when the history is full. A frame
// with fewer than K accesses has K-distance +infinity. LRU-K evicts the frame
// with the LARGEST K-distance. A page touched once by a big scan therefore
// loses against a page that is used repeatedly, even if the scan touched it
// more recently.
type LRUKReplacer struct {
	k         int                  // K (>= 1)
	now       uint64               // timestamp the next RecordAccess will get
	history   map[FrameID][]uint64 // tracked frame -> last <= k access timestamps, oldest first
	evictable map[FrameID]bool     // tracked frame -> evictable flag
	size      int                  // number of evictable tracked frames
}

// NewLRUKReplacer returns an empty LRU-K replacer.
func NewLRUKReplacer(numFrames, k int) *LRUKReplacer {
	if k < 1 {
		panic("storage: LRU-K needs k >= 1")
	}
	return &LRUKReplacer{
		k:         k,
		history:   make(map[FrameID][]uint64, numFrames),
		evictable: make(map[FrameID]bool, numFrames),
	}
}

// RecordAccess appends the current timestamp to f's history (keeping only
// the last k) and advances the clock. Untracked f becomes tracked, not evictable.
func (r *LRUKReplacer) RecordAccess(f FrameID) {
	h, ok := r.history[f]
	if !ok {
		r.evictable[f] = false
	}
	h = append(h, r.now)
	if len(h) > r.k {
		h = h[len(h)-r.k:]
	}
	r.history[f] = h
	r.now++
}

func (r *LRUKReplacer) SetEvictable(f FrameID, evictable bool) {
	if _, ok := r.history[f]; !ok || r.evictable[f] == evictable {
		return
	}
	r.evictable[f] = evictable
	if evictable {
		r.size++
	} else {
		r.size--
	}
}

func (r *LRUKReplacer) Remove(f FrameID) {
	if _, ok := r.history[f]; !ok {
		return
	}
	if r.evictable[f] {
		r.size--
	}
	delete(r.history, f)
	delete(r.evictable, f)
}

func (r *LRUKReplacer) Size() int { return r.size }

// Problem 3.10 — LRUKReplacer.Evict [Hard]
//
// Evict the evictable frame with the largest backward K-distance (frames with fewer than K accesses first).
//
// Rules:
//  1. Only evictable frames are candidates. None -> (0, false).
//  2. A frame with fewer than K recorded accesses has distance +infinity
//     and beats every frame with K accesses.
//  3. Among +infinity frames, evict the one whose FIRST recorded access
//     (history[0]) is the earliest — classic FIFO among cold pages, NOT the
//     least recently used one.
//  4. Among frames with K accesses, evict the one with the largest
//     K-distance, i.e. the smallest history[0] (the K-th most recent access).
//  5. Timestamps are unique, so there are never ties.
//  6. Stop tracking the victim: delete its history and flag, decrement size.
//     If it is accessed again later, its history starts from scratch.
//
// Example (K=2, all frames evictable):
//
//	access 0,1,2,0,1  -> f0=[0 3] f1=[1 4] f2=[2]
//	Evict() -> 2 (+inf), then 0 (history[0]=0 < 1), then 1
//
//	access 0,1,1,0    -> f0=[0 3] f1=[1 2]
//	Evict() -> 0       (plain LRU would pick 1: its last access 2 < 3)
//
//	K=3: access 0,1,0 -> f0=[0 2] f1=[1], both +inf
//	Evict() -> 0       (earliest first access, although 0 was used more recently)
//
// Constraints: O(number of tracked frames) per Evict is fine.
// Postgres reference: none — Postgres uses clock sweep; LRU-K is the classic
// alternative studied for database buffer pools.
func (r *LRUKReplacer) Evict() (FrameID, bool) {
	var (
		victim   FrameID
		found    bool
		bestInf  bool
		bestTime uint64
	)
	for f, h := range r.history {
		if !r.evictable[f] {
			continue
		}
		inf := len(h) < r.k
		better := !found ||
			(inf && !bestInf) ||
			(inf == bestInf && h[0] < bestTime)
		if better {
			victim, found, bestInf, bestTime = f, true, inf, h[0]
		}
	}
	if !found {
		return 0, false
	}
	delete(r.history, victim)
	delete(r.evictable, victim)
	r.size--
	return victim, true
}
