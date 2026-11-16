package heap

// Problem 4.15 — SampleSystem [Medium]
//
// Implement TABLESAMPLE SYSTEM (percent) REPEATABLE (seed): pick whole pages at random and return every live tuple on the picked pages.
//
// SYSTEM sampling is block-level: instead of flipping a coin per row
// (BERNOULLI, which still reads every page), it flips one coin per PAGE and
// reads only the winners — fast, but rows on the same page come together.
//
// Rules:
//  1. percent must satisfy 0 <= percent <= 100 (NaN is invalid), otherwise
//     return (nil, ErrBadPercent) without reading anything.
//  2. Create rng := rand.New(rand.NewPCG(seed, 0)) (math/rand/v2).
//  3. For page = 0, 1, …, NumPages()-1 in order, draw EXACTLY one
//     r := rng.Float64() per page (also for pages that end up not picked).
//     The page is picked iff r*100 < percent. So percent 0 picks nothing
//     and percent 100 picks every page.
//  4. Read each picked page with one ReadPage (never read unpicked pages)
//     and append its live tuples in slot order. The result is in TID order
//     and has length 0 (non-nil) when nothing is picked.
//
// Example (10 pages, page p holds 2 tuples; seed 42):
//
//	draws r for pages 0..9: .253 .545 .130 .266 .753 .179 .100 .797 .041 .332
//	SampleSystem(store, 30, 42) → pages 0 2 3 5 6 8 picked: 12 tuples, 6 reads
//	SampleSystem(store, 30, 42) again → exactly the same pages (REPEATABLE)
//	SampleSystem(store, 30, 7)  → pages 2 3 5 6 (another seed, another sample)
//	SampleSystem(store, 50, 42) → pages 0 2 3 5 6 8 9 (a superset: same draws)
//	SampleSystem(store, 0, 42)   → nothing, 0 reads
//	SampleSystem(store, 100, 42) → all 20 tuples, 10 reads
//	SampleSystem(store, 101, 42) → ErrBadPercent
//
// Constraints: reads = number of picked pages.
// Postgres reference: system_nextsampleblock() in src/backend/access/tablesample/system.c
// (Postgres hashes (block, seed) instead of drawing a sequence; same idea.)
func SampleSystem(store *PageStore, percent float64, seed uint64) ([]Tuple, error) {
	panic("TODO: Problem 4.15")
}
