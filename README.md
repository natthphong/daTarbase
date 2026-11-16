# daTarbase 🐘

**Build a PostgreSQL-style database in Go — one LeetCode-style problem at a time.**

Every function is a problem: a precise statement in its doc comment, an empty body
(`panic("TODO")`), and unit tests with real test data. Solve them all and the pieces
assemble into a small Postgres-like engine: 8KB slotted pages, a buffer pool, heap
files, B+Tree indexes (page split, INCLUDE / covering), a Volcano executor, sorting &
aggregation, inner/left/right/full/semi/anti joins, MVCC transactions with
commit/rollback, and a write-ahead log with crash recovery. Phase 11 glues every
phase into one engine with a Go API — `go run ./cmd/demo` shows it end to end
(on the `solution` branch, or once you have solved everything).

## How to use

```bash
make p P=2.3          # run the tests of one problem (Problem 2.3)
make phase P=02       # run every test of a phase
make progress         # ✅ / ⬜ / ❌ for every problem
make test             # everything
```

1. Pick a problem from the index below and click it — it jumps to the function.
2. Read the doc comment. It contains everything you need; the tests only check it.
3. Replace `panic("TODO: Problem X.Y")` with your implementation.
4. `make p P=X.Y` until green.

Tests of a problem only use provided code and **earlier problems of the same phase**,
so phases 1–10 can be done in any order. Within a phase, go in order.
Phase 11 (the engine) imports all other phases, so do it last.

## Branches

| Branch | Contents |
|---|---|
| `main` | the problem set — bodies are `panic("TODO")` |
| `solution` | reference solutions. Stuck? `git diff main solution -- phase02_page/page.go` |

## Layout

```
pkg/types      shared values, rows, schema, PageID / TID / TxnID / LSN   (provided)
pkg/exec       Volcano Iterator interface + Values() + Collect()          (provided)
pkg/testkit    R(1,"a",nil), AssertRows ... test helpers                  (provided)
cmd/demo       narrated end-to-end demo of the finished engine
phaseNN_*/     the problems — README.md in each phase is a deep-dive on how Postgres does it
tools/dbtool   generates this README, runs `make progress`
```

See [CONVENTIONS.md](CONVENTIONS.md) for how problems and tests are written.

## Problem index

**185 problems** — 🟢 62 Easy · 🟡 89 Medium · 🔴 34 Hard

- [Phase 1 — Tuple Format & Data Types](#phase-1)
- [Phase 2 — Slotted Page](#phase-2)
- [Phase 3 — Disk Manager & Buffer Pool](#phase-3)
- [Phase 4 — Heap File & Table Scans](#phase-4)
- [Phase 5 — B+Tree Index](#phase-5)
- [Phase 6 — Executor: Expressions, Scans, Planner basics](#phase-6)
- [Phase 7 — Sorting & Aggregation](#phase-7)
- [Phase 8 — Joins](#phase-8)
- [Phase 9 — Transactions: MVCC, Isolation, Locks](#phase-9)
- [Phase 10 — Write-Ahead Log & Crash Recovery](#phase-10)
- [Phase 11 — Engine: one database out of ten phases](#phase-11)

<a id="phase-1"></a>

### Phase 1 — Tuple Format & Data Types

How a row becomes bytes on disk, the way Postgres heap tuples do it: a fixed header, an optional null bitmap, and every non-NULL attribute aligned to its type — plus fast single-column access via cached offsets, an order-preserving key encoding for B+Trees, and "column tetris" to stop wasting bytes on padding.

📁 [`phase01_tuple/`](phase01_tuple/) · 📖 [phase notes](phase01_tuple/README.md) · ▶️ `make phase P=01`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 1.1 | [`AlignUp`](phase01_tuple/tuple.go#L84) | 🟢 Easy | Round an offset up to the next multiple of a type's alignment (typalign padding). | [2 tests](phase01_tuple/basics_test.go#L14) |
| 1.2 | [`AppendFixed`](phase01_tuple/tuple.go#L111) | 🟢 Easy | Append the on-disk bytes of a fixed-width value (bool, int4, int8, float8) to buf. | [2 tests](phase01_tuple/basics_test.go#L51) |
| 1.3 | [`DecodeFixed`](phase01_tuple/tuple.go#L136) | 🟢 Easy | Decode a fixed-width value of the given kind from the start of src. | [2 tests](phase01_tuple/basics_test.go#L91) |
| 1.4 | [`AppendVarlena`](phase01_tuple/tuple.go#L158) | 🟡 Medium | Append a text value as a Postgres-style varlena: a length header followed by the bytes. | [3 tests](phase01_tuple/basics_test.go#L150) |
| 1.5 | [`DecodeVarlena`](phase01_tuple/tuple.go#L190) | 🟡 Medium | Decode the varlena at the start of src; return the string and the total bytes it occupies. | [3 tests](phase01_tuple/basics_test.go#L177) |
| 1.6 | [`NullBitmap`](phase01_tuple/tuple.go#L218) | 🟢 Easy | Build the null bitmap for a row: bit i set means attribute i is NOT NULL; nil if no NULLs. | [1 test](phase01_tuple/basics_test.go#L226) |
| 1.7 | [`AttIsNull`](phase01_tuple/tuple.go#L244) | 🟢 Easy | Report whether attribute i is NULL according to a null bitmap. | [2 tests](phase01_tuple/basics_test.go#L255) |
| 1.8 | [`AppendHeader`](phase01_tuple/tuple.go#L263) | 🟡 Medium | Append the 13-byte fixed tuple header to buf. | [3 tests](phase01_tuple/basics_test.go#L291) |
| 1.9 | [`DecodeHeader`](phase01_tuple/tuple.go#L292) | 🟡 Medium | Decode and sanity-check the fixed header at the start of a tuple. | [3 tests](phase01_tuple/basics_test.go#L314) |
| 1.10 | [`ComputeTupleSize`](phase01_tuple/form.go#L34) | 🟡 Medium | Compute the exact byte length FormTuple will produce for a row, without forming it. | [3 tests](phase01_tuple/form_test.go#L13) |
| 1.11 | [`FormTuple`](phase01_tuple/form.go#L69) | 🔴 Hard | Serialize a row into a heap tuple: header + optional null bitmap + aligned attributes. | [4 tests](phase01_tuple/form_test.go#L55) |
| 1.12 | [`DeformTuple`](phase01_tuple/form.go#L109) | 🔴 Hard | Parse a heap tuple produced by FormTuple back into a row. | [5 tests](phase01_tuple/form_test.go#L153) |
| 1.13 | [`GetAttr`](phase01_tuple/form.go#L150) | 🟡 Medium | Fetch ONE attribute of a tuple, using cached column offsets to skip the walk when possible. | [5 tests](phase01_tuple/form_test.go#L242) |
| 1.14 | [`EncodeKey`](phase01_tuple/key.go#L13) | 🔴 Hard | Encode a list of values into "memcomparable" bytes whose bytes.Compare order equals SQL ORDER BY. | [4 tests](phase01_tuple/key_test.go#L15) |
| 1.15 | [`ColumnTetris`](phase01_tuple/tetris.go#L7) | 🟡 Medium | Reorder a table's columns to minimize alignment padding and report the bytes saved per row. | [3 tests](phase01_tuple/tetris_test.go#L11) |

<a id="phase-2"></a>

### Phase 2 — Slotted Page

Build the 8 KB slotted page that every Postgres table and index is made of: a small header, an array of line pointers growing up from the front, tuple bytes growing down from the back, and the free hole in between. You will encode line pointers with Postgres' `LP_UNUSED / LP_NORMAL / LP_DEAD` states, insert (recycling free slots), read, scan, delete, update in place, defragment without changing any tuple's slot number, honour a table's fillfactor, checksum the page, load it back from disk defensively, and dump it like `pageinspect`.

📁 [`phase02_page/`](phase02_page/) · 📖 [phase notes](phase02_page/README.md) · ▶️ `make phase P=02`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 2.1 | [`SetItemID`](phase02_page/lines.go#L3) | 🟢 Easy | Encode a line pointer and write it into an existing slot of the line-pointer array. | [1 test](phase02_page/lines_test.go#L9) |
| 2.2 | [`ItemID`](phase02_page/lines.go#L42) | 🟢 Easy | Decode the line pointer of `slot` into an ItemID. | [2 tests](phase02_page/lines_test.go#L50) |
| 2.3 | [`FreeSpace`](phase02_page/lines.go#L67) | 🟢 Easy | Return the size of the largest tuple InsertTuple could store right now. | [1 test](phase02_page/lines_test.go#L95) |
| 2.4 | [`GetTuple`](phase02_page/tuple.go#L5) | 🟢 Easy | Return the bytes of the live tuple stored in `slot`. | [2 tests](phase02_page/lines_test.go#L121) |
| 2.5 | [`InsertTuple`](phase02_page/tuple.go#L34) | 🟡 Medium | Insert `data` into the page and return its slot, recycling an UNUSED slot before growing the array. | [7 tests](phase02_page/insert_tuple_test.go#L10) |
| 2.6 | [`Tuples`](phase02_page/tuple.go#L76) | 🟢 Easy | Return an iterator over every live (LPNormal) tuple as (slot, bytes), in ascending slot order. | [3 tests](phase02_page/tuple_test.go#L22) |
| 2.7 | [`DeleteTuple`](phase02_page/tuple.go#L103) | 🟢 Easy | Mark the tuple in `slot` as deleted by flipping its line pointer to LP_DEAD. | [2 tests](phase02_page/tuple_test.go#L80) |
| 2.8 | [`UpdateTuple`](phase02_page/tuple.go#L132) | 🟡 Medium | Overwrite the tuple in `slot` in place when the new version is not longer than the old one. | [3 tests](phase02_page/tuple_test.go#L136) |
| 2.9 | [`Compact`](phase02_page/compact.go#L3) | 🔴 Hard | Defragment the page: drop dead tuples, pack live tuples at the end, keep every slot number. | [4 tests](phase02_page/compact_test.go#L29) |
| 2.10 | [`InsertWithFillFactor`](phase02_page/compact.go#L51) | 🟡 Medium | Insert like InsertTuple, but refuse if the page would end up more than fillfactor% full. | [3 tests](phase02_page/compact_test.go#L242) |
| 2.11 | [`ComputeChecksum`](phase02_page/checksum.go#L7) | 🟡 Medium | Compute the 16-bit checksum of the page as it would be stored on disk as block `id`. | [3 tests](phase02_page/checksum_test.go#L21) |
| 2.12 | [`VerifyChecksum`](phase02_page/checksum.go#L43) | 🟢 Easy | Check that the stored checksum matches the page contents for block `id`. | [1 test](phase02_page/checksum_test.go#L81) |
| 2.13 | [`Serialize`](phase02_page/checksum.go#L71) | 🟢 Easy | Return a private copy of the page image, with the checksum for block `id` stamped into the copy. | [2 tests](phase02_page/checksum_test.go#L101) |
| 2.14 | [`Load`](phase02_page/checksum.go#L98) | 🟡 Medium | Build a Page from an on-disk image of block `id`, rejecting torn or corrupt pages. | [3 tests](phase02_page/checksum_test.go#L144) |
| 2.15 | [`Dump`](phase02_page/dump.go#L3) | 🟢 Easy | Render the page header and every line pointer as a human-readable table, like pageinspect. | [2 tests](phase02_page/checksum_test.go#L228) |

<a id="phase-3"></a>

### Phase 3 — Disk Manager & Buffer Pool

Build the bottom of the database: a disk manager that reads and writes 8 KB pages in relation files (including Postgres-style 1 GB segment files), and a buffer pool that caches those pages in a fixed set of memory frames, with pinning, dirty tracking, write-back, three replacement policies (LRU, Postgres's clock sweep, LRU-K) and a ring-buffer strategy that stops big sequential scans from wiping the cache.

📁 [`phase03_storage/`](phase03_storage/) · 📖 [phase notes](phase03_storage/README.md) · ▶️ `make phase P=03`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 3.1 | [`FileDisk.ReadPage`](phase03_storage/filedisk.go#L60) | 🟢 Easy | Read page id of a single-file relation into buf. | [4 tests](phase03_storage/filedisk_test.go#L87) |
| 3.2 | [`FileDisk.WritePage`](phase03_storage/filedisk.go#L94) | 🟢 Easy | Write buf over the existing page id, retrying short writes until all 8192 bytes are on disk. | [4 tests](phase03_storage/filedisk_test.go#L185) |
| 3.3 | [`FileDisk.AllocatePage`](phase03_storage/filedisk.go#L127) | 🟢 Easy | Extend the file by one zero-filled page and return the new page's id. | [3 tests](phase03_storage/filedisk_test.go#L278) |
| 3.4 | [`SegmentFor`](phase03_storage/segment.go#L106) | 🟢 Easy | Map a relation-wide page number to (segment number, page number inside that segment). | [1 test](phase03_storage/segment_test.go#L12) |
| 3.5 | [`SegmentedDisk.Locate`](phase03_storage/segment.go#L129) | 🟢 Easy | Find the open segment file and local page number that hold global page id. | [2 tests](phase03_storage/segment_test.go#L61) |
| 3.6 | [`SegmentedDisk.AllocatePage`](phase03_storage/segment.go#L155) | 🟡 Medium | Append a zeroed page to the relation, creating the next segment file when the last one is full. | [3 tests](phase03_storage/segment_test.go#L129) |
| 3.7 | [`LRUReplacer.RecordAccess`](phase03_storage/lru.go#L98) | 🟢 Easy | Record that frame f was just used, making it the most recently used frame, in O(1). | [2 tests](phase03_storage/replacer_test.go#L89) |
| 3.8 | [`LRUReplacer.Evict`](phase03_storage/lru.go#L119) | 🟡 Medium | Remove and return the least recently used evictable frame. | [3 tests](phase03_storage/replacer_test.go#L139) |
| 3.9 | [`ClockReplacer.Evict`](phase03_storage/clock.go#L65) | 🟡 Medium | Run Postgres's clock sweep: advance the hand, decrementing usage counts, until an evictable frame with usage 0 is found. | [5 tests](phase03_storage/replacer_test.go#L205) |
| 3.10 | [`LRUKReplacer.Evict`](phase03_storage/lruk.go#L78) | 🔴 Hard | Evict the evictable frame with the largest backward K-distance (frames with fewer than K accesses first). | [3 tests](phase03_storage/replacer_test.go#L300) |
| 3.11 | [`UnpinPage`](phase03_storage/bufferpool.go#L96) | 🟢 Easy | Release one pin on a resident page, remembering whether the caller modified it. | [2 tests](phase03_storage/bufferpool_test.go#L119) |
| 3.12 | [`acquireFrame`](phase03_storage/bufferpool.go#L124) | 🟡 Medium | Find a frame to load a new page into: a free frame if any, else evict a victim (writing it back if dirty). | [5 tests](phase03_storage/bufferpool_test.go#L165) |
| 3.13 | [`FetchPage`](phase03_storage/bufferpool.go#L156) | 🔴 Hard | Return the pinned frame holding page id, reading it from disk on a cache miss; safe for concurrent callers. | [5 tests](phase03_storage/bufferpool_test.go#L267) |
| 3.14 | [`NewPage`](phase03_storage/bufferpool.go#L190) | 🟡 Medium | Allocate a brand-new page on disk and return it pinned in a zeroed frame. | [3 tests](phase03_storage/bufferpool_test.go#L450) |
| 3.15 | [`FlushPage`](phase03_storage/bufferpool.go#L216) | 🟢 Easy | Write a resident page to disk if it is dirty, and mark it clean. | [2 tests](phase03_storage/bufferpool_test.go#L531) |
| 3.16 | [`FlushAll`](phase03_storage/bufferpool.go#L241) | 🟢 Easy | Write every dirty resident page to disk in increasing page-id order (what a checkpoint does). | [1 test](phase03_storage/bufferpool_test.go#L589) |
| 3.17 | [`FetchPageWithRing`](phase03_storage/ring.go#L30) | 🔴 Hard | FetchPage for a bulk sequential scan: on a miss, recycle the frame in the ring's next slot so the scan cannot flush the whole cache. | [6 tests](phase03_storage/ring_test.go#L29) |

<a id="phase-4"></a>

### Phase 4 — Heap File & Table Scans

Build a table's heap file — an unordered bag of pages holding tuples — and every way Postgres reads it: a free space map (flat, then as a real max-tree) that tells inserts where room is, insert / fetch / delete / update with HOT-like same-page updates and ctid forwarding chains, then the scan family: sequential, synchronized (wrap-around), TID range, bitmap heap scan and TABLESAMPLE SYSTEM, plus TOAST for values too big to fit in a page.

📁 [`phase04_heap/`](phase04_heap/) · 📖 [phase notes](phase04_heap/README.md) · ▶️ `make phase P=04`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 4.1 | [`FSMUpdate`](phase04_heap/fsm.go#L40) | 🟢 Easy | Record that heap page `page` now has `free` bytes available, as a 1-byte category. | [1 test](phase04_heap/fsm_test.go#L12) |
| 4.2 | [`FSMSearch`](phase04_heap/fsm.go#L69) | 🟢 Easy | Return the lowest-numbered page whose category promises at least `need` free bytes. | [2 tests](phase04_heap/fsm_test.go#L49) |
| 4.3 | [`FSMTreeSet`](phase04_heap/fsm.go#L139) | 🟡 Medium | Set the category of one page's leaf and repair the max-tree above it. | [3 tests](phase04_heap/fsm_test.go#L93) |
| 4.4 | [`FSMTreeSearch`](phase04_heap/fsm.go#L166) | 🔴 Hard | Find, in O(log n), the first page at or after `start` (wrapping around) whose category is >= minCat. | [3 tests](phase04_heap/fsm_test.go#L168) |
| 4.5 | [`Heap.Insert`](phase04_heap/heap.go#L16) | 🟡 Medium | Store a tuple somewhere in the heap — on a page the FSM says has room, otherwise on a brand-new page — and return its TID. | [4 tests](phase04_heap/heap_test.go#L11) |
| 4.6 | [`Heap.Fetch`](phase04_heap/heap.go#L58) | 🟢 Easy | Return the newest version of the tuple at `tid`, following forward (ctid) pointers left by updates. | [1 test](phase04_heap/heap_test.go#L158) |
| 4.7 | [`Heap.Delete`](phase04_heap/heap.go#L87) | 🟢 Easy | Mark the tuple version at `tid` dead. | [2 tests](phase04_heap/heap_test.go#L196) |
| 4.8 | [`Heap.Update`](phase04_heap/heap.go#L116) | 🟡 Medium | Write a new version of the tuple at `tid`: on the same page if it fits (HOT-like), elsewhere otherwise, and chain the old version to it. | [3 tests](phase04_heap/heap_test.go#L250) |
| 4.9 | [`SeqScan.Next`](phase04_heap/scan.go#L23) | 🟡 Medium | Return the next live tuple of a sequential scan: page by page, slot by slot, skipping dead slots. | [3 tests](phase04_heap/scan_test.go#L9) |
| 4.10 | [`SyncScan`](phase04_heap/scan.go#L83) | 🟡 Medium | Return the next live tuple of a synchronized scan that starts at page `start`, wraps around, and publishes its position. | [4 tests](phase04_heap/scan_test.go#L102) |
| 4.11 | [`TIDRangeScan`](phase04_heap/scan.go#L118) | 🟢 Easy | Return every live tuple whose TID lies in the inclusive range [lo, hi], in TID order, reading only the pages that can contain them. | [2 tests](phase04_heap/scan_test.go#L202) |
| 4.12 | [`BitmapHeapScan`](phase04_heap/scan.go#L150) | 🟡 Medium | Fetch the tuples an index found — given as an unsorted TID list plus some lossy pages — visiting each heap page at most once, in page order. | [3 tests](phase04_heap/scan_test.go#L243) |
| 4.13 | [`Toast`](phase04_heap/toast.go#L43) | 🟡 Medium | Encode a column value for storage: small values inline, large ones split into chunks in the TOAST heap behind a 9-byte pointer. | [3 tests](phase04_heap/toast_test.go#L23) |
| 4.14 | [`Detoast`](phase04_heap/toast.go#L83) | 🟡 Medium | Turn a stored value produced by Toast back into the original bytes, reassembling and validating TOAST chunks. | [3 tests](phase04_heap/toast_test.go#L130) |
| 4.15 | [`SampleSystem`](phase04_heap/sample.go#L3) | 🟡 Medium | Implement TABLESAMPLE SYSTEM (percent) REPEATABLE (seed): pick whole pages at random and return every live tuple on the picked pages. | [4 tests](phase04_heap/sample_test.go#L31) |

<a id="phase-5"></a>

### Phase 5 — B+Tree Index

Build the index structure behind almost every Postgres index: a B+Tree (`nbtree`) over memcomparable byte keys that maps keys to heap TIDs. You implement search, inserts that split pages up to the root, range scans in both directions, prefix scans over composite keys, covering (INCLUDE) index-only scans, deletes that rebalance, `CREATE INDEX`-style bulk loading, a page format for nodes, and an `amcheck`-style validator. This is how `WHERE id = 42`, `ORDER BY ... LIMIT` and `WHERE a = 1 AND b > 5` avoid reading the whole table.

📁 [`phase05_btree/`](phase05_btree/) · 📖 [phase notes](phase05_btree/README.md) · ▶️ `make phase P=05`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 5.1 | [`CompareItems`](phase05_btree/search.go#L7) | 🟢 Easy | Order two index tuples by (Key, TID): the "heapkeyspace" rule that makes every entry unique. | [1 test](phase05_btree/search_test.go#L10) |
| 5.2 | [`LowerBound`](phase05_btree/search.go#L38) | 🟢 Easy | Binary-search a node for the first item that is >= probe. | [2 tests](phase05_btree/search_test.go#L58) |
| 5.3 | [`UpperBound`](phase05_btree/search.go#L64) | 🟢 Easy | Binary-search a node for the first item that is > probe. | [2 tests](phase05_btree/search_test.go#L87) |
| 5.4 | [`Descend`](phase05_btree/search.go#L91) | 🟡 Medium | Walk from the root to the leaf that owns probe, recording the path (a parent stack). | [3 tests](phase05_btree/search_test.go#L139) |
| 5.5 | [`Search`](phase05_btree/search.go#L124) | 🟡 Medium | Return the TIDs of every entry whose key equals key, following the leaf chain across duplicates. | [3 tests](phase05_btree/search_test.go#L208) |
| 5.6 | [`InsertItem`](phase05_btree/insert.go#L7) | 🟡 Medium | Insert an entry into an in-memory leaf at its sorted position, without splitting. | [3 tests](phase05_btree/insert_test.go#L13) |
| 5.7 | [`SplitLeaf`](phase05_btree/insert.go#L36) | 🔴 Hard | Split an overflowing leaf in two, link the new right sibling into the leaf chain, and return the separator to copy up. | [3 tests](phase05_btree/insert_test.go#L75) |
| 5.8 | [`SplitInternal`](phase05_btree/insert.go#L71) | 🔴 Hard | Split an overflowing internal node in two and push its middle separator up to the parent. | [3 tests](phase05_btree/insert_test.go#L150) |
| 5.9 | [`GrowRoot`](phase05_btree/insert.go#L105) | 🟢 Easy | The root split: put a new root above the old one, growing the tree by one level. | [1 test](phase05_btree/insert_test.go#L194) |
| 5.10 | [`Insert`](phase05_btree/insert.go#L129) | 🔴 Hard | Insert an entry into the tree, splitting nodes bottom-up and growing the root when needed. | [4 tests](phase05_btree/insert_test.go#L226) |
| 5.11 | [`InsertUnique`](phase05_btree/insert.go#L166) | 🟢 Easy | Insert into a UNIQUE index: reject the entry if any entry with the same key exists. | [2 tests](phase05_btree/insert_test.go#L350) |
| 5.12 | [`Cursor.Next`](phase05_btree/scan.go#L7) | 🟡 Medium | Return the next entry of a range scan, walking the leaf chain forward (ASC) or backward (DESC). | [5 tests](phase05_btree/scan_test.go#L26) |
| 5.13 | [`Prefix`](phase05_btree/scan.go#L55) | 🟡 Medium | Return an ascending cursor over every entry whose key starts with prefix (composite-key / LIKE 'abc%' scan). | [3 tests](phase05_btree/scan_test.go#L193) |
| 5.14 | [`IndexOnlyScan`](phase05_btree/scan.go#L87) | 🟢 Easy | Answer a range query from the index alone (key + INCLUDE payload), visiting the heap only for pages that are not all-visible. | [1 test](phase05_btree/scan_test.go#L318) |
| 5.15 | [`RemoveEntry`](phase05_btree/delete.go#L7) | 🟡 Medium | Remove one exact (key, tid) entry from its leaf — found by a single descent even among thousands of duplicates. | [3 tests](phase05_btree/delete_test.go#L27) |
| 5.16 | [`Borrow`](phase05_btree/delete.go#L37) | 🟡 Medium | Fix an underfull child by moving one item from a sibling that can spare it (rotation through the parent). | [3 tests](phase05_btree/delete_test.go#L98) |
| 5.17 | [`Merge`](phase05_btree/delete.go#L82) | 🟡 Medium | Merge an underfull child with a sibling into one node and remove the separator between them from the parent. | [3 tests](phase05_btree/delete_test.go#L185) |
| 5.18 | [`Delete`](phase05_btree/delete.go#L116) | 🔴 Hard | Delete one exact (key, tid) entry and rebalance bottom-up: borrow, else merge, and shrink the root. | [5 tests](phase05_btree/delete_test.go#L235) |
| 5.19 | [`BulkLoad`](phase05_btree/bulk.go#L3) | 🔴 Hard | Build a tree from sorted entries the way CREATE INDEX does: fill leaves left to right, then build each upper level bottom-up. | [6 tests](phase05_btree/bulk_test.go#L19) |
| 5.20 | [`EncodePage`](phase05_btree/page.go#L7) | 🟡 Medium | Serialize a node into one 8192-byte slotted page: header, line pointers, and index tuples. | [3 tests](phase05_btree/page_test.go#L34) |
| 5.21 | [`DecodePage`](phase05_btree/page.go#L53) | 🟡 Medium | Parse a page written by EncodePage back into a Node, rejecting anything malformed. | [5 tests](phase05_btree/page_test.go#L111) |
| 5.22 | [`Validate`](phase05_btree/validate.go#L3) | 🟡 Medium | Check every structural invariant of the tree and report the first violation (an amcheck for your index). | [3 tests](phase05_btree/validate_test.go#L13) |

<a id="phase-6"></a>

### Phase 6 — Executor: Expressions, Scans, Planner basics

The streaming half of the executor and the first planner decisions: an expression evaluator with SQL three-valued logic, the pipelined Filter / Projection / Limit operators, every single-table access path Postgres has (Seq Scan, Index Scan, Index Only Scan with the visibility map, Bitmap Index Scan + BitmapAnd/BitmapOr + Bitmap Heap Scan with lossy pages), and the planner pieces that choose between them: selectivity estimation from `pg_statistic`, the `costsize.c` cost model, EXPLAIN output and constant folding. Everything runs against small instrumented in-memory fakes (heap table, B-tree index, visibility map) so the tests can count page reads and heap fetches.

📁 [`phase06_executor/`](phase06_executor/) · 📖 [phase notes](phase06_executor/README.md) · ▶️ `make phase P=06`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 6.1 | [`Eval`](phase06_executor/eval.go#L7) | 🟡 Medium | Evaluate an expression tree against one row using SQL three-valued logic (TRUE / FALSE / NULL). | [6 tests](phase06_executor/eval_test.go#L29) |
| 6.2 | [`Filter`](phase06_executor/operators.go#L24) | 🟢 Easy | Return the next child row for which the predicate evaluates to TRUE. | [3 tests](phase06_executor/operators_test.go#L12) |
| 6.3 | [`Project`](phase06_executor/operators.go#L60) | 🟢 Easy | Return the next child row transformed by the SELECT-list expressions. | [3 tests](phase06_executor/operators_test.go#L81) |
| 6.4 | [`Limit`](phase06_executor/operators.go#L96) | 🟢 Easy | Skip Offset rows, then return at most Count rows — and stop pulling the child as soon as the limit is reached. | [3 tests](phase06_executor/operators_test.go#L126) |
| 6.5 | [`SeqScan`](phase06_executor/scans.go#L29) | 🟢 Easy | Return the next live row of the table in (page, slot) order that passes the pushed-down qual. | [4 tests](phase06_executor/scans_test.go#L15) |
| 6.6 | [`IndexScan`](phase06_executor/scans.go#L78) | 🟡 Medium | Return the next heap row whose indexed key lies in Range, in index order, fetching rows by TID. | [4 tests](phase06_executor/scans_test.go#L103) |
| 6.7 | [`IndexOnlyScan`](phase06_executor/scans.go#L134) | 🟡 Medium | Return the next in-range key as a one-column row, touching the heap only for pages that are not all-visible. | [3 tests](phase06_executor/scans_test.go#L204) |
| 6.8 | [`BitmapIndexScan`](phase06_executor/bitmap.go#L104) | 🟡 Medium | Collect the TIDs of every index entry in a key range into a TID bitmap, going lossy when it exceeds a memory budget. | [3 tests](phase06_executor/bitmap_test.go#L45) |
| 6.9 | [`BitmapCombine`](phase06_executor/bitmap.go#L144) | 🟡 Medium | Intersect (BitmapAnd) or union (BitmapOr) two TID bitmaps, handling lossy pages and recheck flags. | [3 tests](phase06_executor/bitmap_test.go#L90) |
| 6.10 | [`BitmapHeapScan`](phase06_executor/bitmap.go#L204) | 🟡 Medium | Return the rows named by the bitmap in physical (page, slot) order, reading each heap page once and rechecking where needed. | [4 tests](phase06_executor/bitmap_test.go#L161) |
| 6.11 | [`EqSel`](phase06_executor/selectivity.go#L27) | 🟡 Medium | Estimate the fraction of rows satisfying "col = c" from the column statistics. | [3 tests](phase06_executor/selectivity_test.go#L22) |
| 6.12 | [`IneqSel`](phase06_executor/selectivity.go#L63) | 🔴 Hard | Estimate the fraction of rows satisfying "col op c" for op in <, <=, >, >= using MCVs plus an equi-depth histogram. | [3 tests](phase06_executor/selectivity_test.go#L66) |
| 6.13 | [`BetweenSel`](phase06_executor/selectivity.go#L106) | 🟢 Easy | Estimate "col BETWEEN lo AND hi" by combining the two one-sided estimates the way Postgres combines a range pair. | [1 test](phase06_executor/selectivity_test.go#L142) |
| 6.14 | [`ChooseScan`](phase06_executor/cost.go#L45) | 🟡 Medium | Cost a Seq Scan, an Index Scan and a Bitmap Heap Scan for one qual and pick the cheapest, Postgres-style. | [4 tests](phase06_executor/cost_test.go#L13) |
| 6.15 | [`Explain`](phase06_executor/explain.go#L22) | 🟢 Easy | Render a plan tree as indented text in the format of Postgres' EXPLAIN. | [2 tests](phase06_executor/explain_test.go#L8) |
| 6.16 | [`FoldConstants`](phase06_executor/fold.go#L3) | 🟡 Medium | Simplify an expression at plan time: evaluate constant sub-trees and apply the safe boolean/NULL shortcuts. | [4 tests](phase06_executor/fold_test.go#L10) |

<a id="phase-7"></a>

### Phase 7 — Sorting & Aggregation

The executor operators that cannot simply stream rows through: ORDER BY (in-memory stable sort, top-N heapsort for `ORDER BY … LIMIT`, external merge sort that spills to temp files, incremental sort), GROUP BY (hash aggregation and streaming sorted aggregation with COUNT/SUM/AVG/MIN/MAX, DISTINCT aggregates and HAVING), SELECT DISTINCT (hash and Unique), and window functions (ROW_NUMBER, RANK, DENSE_RANK, running SUM). They all use the Volcano `exec.Iterator` interface from `pkg/exec` and one shared comparator.

📁 [`phase07_sortagg/`](phase07_sortagg/) · 📖 [phase notes](phase07_sortagg/README.md) · ▶️ `make phase P=07`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 7.1 | [`CompareRows`](phase07_sortagg/sortagg.go#L32) | 🟡 Medium | Compare two rows under a list of ORDER BY keys, honouring ASC/DESC and NULLS FIRST/LAST. | [3 tests](phase07_sortagg/compare_rows_test.go#L21) |
| 7.2 | [`Sort.Open`](phase07_sortagg/sort.go#L22) | 🟢 Easy | Open the child, read every row from it and sort them in memory, stably. | [3 tests](phase07_sortagg/sort_test.go#L31) |
| 7.3 | [`TopN.Open`](phase07_sortagg/sort.go#L92) | 🟡 Medium | Compute ORDER BY Keys LIMIT N while holding at most N rows in memory. | [3 tests](phase07_sortagg/sort_test.go#L94) |
| 7.4 | [`GenerateRuns`](phase07_sortagg/runs.go#L249) | 🟡 Medium | Phase 1 of an external sort: cut the input into sorted runs of at most memRows rows, each written to its own temp file. | [4 tests](phase07_sortagg/runs_test.go#L58) |
| 7.5 | [`MergeRuns`](phase07_sortagg/runs.go#L284) | 🔴 Hard | Merge k sorted runs into ONE new sorted run with a min-heap of run heads, reading each input exactly once. | [3 tests](phase07_sortagg/runs_test.go#L158) |
| 7.6 | [`MergeAll`](phase07_sortagg/runs.go#L317) | 🟡 Medium | Phase 2 of an external sort: merge runs in passes, at most fanIn at a time, until a single sorted run remains. | [3 tests](phase07_sortagg/runs_test.go#L245) |
| 7.7 | [`ExternalSort.Open`](phase07_sortagg/runs.go#L364) | 🟡 Medium | Sort the child's rows using at most MemRows rows of memory by generating runs and merging them. | [3 tests](phase07_sortagg/runs_test.go#L372) |
| 7.8 | [`Accum.Advance`](phase07_sortagg/agg.go#L49) | 🟡 Medium | Feed one input row into an aggregate's transition state (COUNT/SUM/AVG/MIN/MAX, optionally DISTINCT). | [3 tests](phase07_sortagg/agg_test.go#L55) |
| 7.9 | [`Accum.Finalize`](phase07_sortagg/agg.go#L82) | 🟢 Easy | Turn a transition state into the aggregate's SQL result value. | [1 test](phase07_sortagg/agg_test.go#L130) |
| 7.10 | [`HashAggregate`](phase07_sortagg/agg.go#L108) | 🟡 Medium | GROUP BY with a hash table of groups → transition states, plus a HAVING filter on the finished rows. | [3 tests](phase07_sortagg/agg_test.go#L174) |
| 7.11 | [`GroupAggregate.Next`](phase07_sortagg/agg.go#L177) | 🟡 Medium | Return the next group's result row, pulling from the sorted child only until the group ends. | [3 tests](phase07_sortagg/agg_test.go#L249) |
| 7.12 | [`HashDistinct.Next`](phase07_sortagg/distinct.go#L32) | 🟢 Easy | Return the next input row whose whole-row value has not been returned before. | [3 tests](phase07_sortagg/distinct_test.go#L12) |
| 7.13 | [`Unique.Next`](phase07_sortagg/distinct.go#L75) | 🟢 Easy | Return the next input row that differs from the previously returned row (sorted DISTINCT). | [3 tests](phase07_sortagg/distinct_test.go#L64) |
| 7.14 | [`IncrementalSort.Next`](phase07_sortagg/incsort.go#L41) | 🟡 Medium | Return rows ordered by all Keys, sorting one presorted-prefix group at a time. | [4 tests](phase07_sortagg/incsort_test.go#L20) |
| 7.15 | [`Window`](phase07_sortagg/window.go#L30) | 🔴 Hard | Evaluate ROW_NUMBER, RANK, DENSE_RANK and running SUM over (PARTITION BY ... ORDER BY ...), appending one column per function. | [5 tests](phase07_sortagg/window_test.go#L27) |

<a id="phase-8"></a>

### Phase 8 — Joins

The three physical join algorithms of the executor — nested loop, hash join and merge join — for every SQL join type (INNER, LEFT, RIGHT, FULL, SEMI, ANTI), plus the NULL trap of `NOT IN`, Grace hash partitioning for inputs bigger than memory, an index nested loop, and the planner side: estimating a join's row count and choosing the cheapest join order with Selinger's dynamic programming.

📁 [`phase08_join/`](phase08_join/) · 📖 [phase notes](phase08_join/README.md) · ▶️ `make phase P=08`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 8.1 | [`JoinSpec.Match`](phase08_join/join.go#L220) | 🟢 Easy | Decide whether a left row and a right row satisfy the ON clause of the join. | [3 tests](phase08_join/match_test.go#L11) |
| 8.2 | [`NestedLoopInner`](phase08_join/nestloop.go#L22) | 🟢 Easy | Compute an INNER join by scanning the whole inner input once per outer row. | [4 tests](phase08_join/nestloop_test.go#L77) |
| 8.3 | [`NestedLoopLeft`](phase08_join/nestloop.go#L67) | 🟡 Medium | Compute a LEFT OUTER join with a nested loop: unmatched outer rows are padded with NULLs. | [3 tests](phase08_join/nestloop_test.go#L138) |
| 8.4 | [`NestedLoopRightFull`](phase08_join/nestloop.go#L107) | 🟡 Medium | Compute a RIGHT or FULL outer join with a nested loop by tracking which inner rows ever matched. | [3 tests](phase08_join/nestloop_test.go#L198) |
| 8.5 | [`NestedLoopSemiAnti`](phase08_join/nestloop.go#L152) | 🟡 Medium | Compute a SEMI join (EXISTS) or ANTI join (NOT EXISTS), stopping each inner scan at the first match. | [3 tests](phase08_join/nestloop_test.go#L261) |
| 8.6 | [`NotIn`](phase08_join/nestloop.go#L187) | 🟡 Medium | Evaluate `outer.col NOT IN (SELECT inner.col FROM inner)` with SQL's three-valued logic. | [3 tests](phase08_join/nestloop_test.go#L316) |
| 8.7 | [`HashJoinInner`](phase08_join/hashjoin.go#L13) | 🟡 Medium | Compute an INNER equi-join by hashing the build (right) input and probing it with each left row. | [4 tests](phase08_join/hashjoin_test.go#L31) |
| 8.8 | [`HashJoinOuter`](phase08_join/hashjoin.go#L54) | 🔴 Hard | Compute LEFT, RIGHT and FULL hash joins using a "matched" flag on every build-side row. | [4 tests](phase08_join/hashjoin_test.go#L101) |
| 8.9 | [`GraceHashJoin`](phase08_join/hashjoin.go#L117) | 🔴 Hard | Join inputs too big for memory by hash-partitioning both sides, then joining partition pairs one at a time. | [6 tests](phase08_join/hashjoin_test.go#L174) |
| 8.10 | [`MergeJoinInner`](phase08_join/mergejoin.go#L13) | 🔴 Hard | Compute an INNER equi-join of two sorted inputs in one merge pass, re-reading duplicate runs with mark/restore. | [5 tests](phase08_join/mergejoin_test.go#L15) |
| 8.11 | [`MergeJoinOuter`](phase08_join/mergejoin.go#L58) | 🔴 Hard | Compute LEFT and FULL merge joins: emit unmatched rows as the merge passes them. | [3 tests](phase08_join/mergejoin_test.go#L115) |
| 8.12 | [`IndexNestedLoopJoin`](phase08_join/indexjoin.go#L8) | 🟡 Medium | Join each outer row with the inner rows found by ONE index lookup on the join key, instead of rescanning the inner. | [4 tests](phase08_join/indexjoin_test.go#L12) |
| 8.13 | [`EstimateEquiJoinRows`](phase08_join/planner.go#L11) | 🟢 Easy | Estimate the row count of R ⨝ S ON R.a = S.b from table sizes and distinct counts. | [1 test](phase08_join/planner_test.go#L11) |
| 8.14 | [`BestJoinOrder`](phase08_join/planner.go#L48) | 🔴 Hard | Find the cheapest left-deep join order of n tables with dynamic programming over table subsets (Selinger). | [3 tests](phase08_join/planner_test.go#L48) |

<a id="phase-9"></a>

### Phase 9 — Transactions: MVCC, Isolation, Locks

How Postgres lets many transactions read and write the same table at once without readers blocking writers: a 2-bit-per-xid commit log, wraparound-safe xid arithmetic, MVCC snapshots and the `HeapTupleSatisfiesMVCC` visibility rules, UPDATE as "stamp xmax + append a new version", READ COMMITTED vs REPEATABLE READ, write-write conflicts and EvalPlanQual, savepoints as subtransactions, the heavyweight lock manager with deadlock detection, and finally VACUUM and FREEZE that clean up after it all.

📁 [`phase09_txn/`](phase09_txn/) · 📖 [phase notes](phase09_txn/README.md) · ▶️ `make phase P=09`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 9.1 | [`ClogSet`](phase09_txn/clog.go#L82) | 🟢 Easy | Store the 2-bit status of xid in the packed commit log. | [2 tests](phase09_txn/clog_test.go#L10) |
| 9.2 | [`ClogGet`](phase09_txn/clog.go#L119) | 🟢 Easy | Read back the status of xid from the packed commit log. | [2 tests](phase09_txn/clog_test.go#L75) |
| 9.3 | [`TxnIDPrecedes`](phase09_txn/clog.go#L146) | 🟢 Easy | Compare two xids the way Postgres does: modulo 2^32, so the counter can wrap around. | [1 test](phase09_txn/clog_test.go#L136) |
| 9.4 | [`TxnManager.Begin`](phase09_txn/manager.go#L60) | 🟢 Easy | Start a transaction: hand out the next xid, mark it running and in progress. | [3 tests](phase09_txn/manager_test.go#L11) |
| 9.5 | [`TxnManager.End`](phase09_txn/manager.go#L87) | 🟢 Easy | Commit (commit=true) or abort (commit=false) a top-level transaction and all its running subtransactions. | [4 tests](phase09_txn/manager_test.go#L66) |
| 9.6 | [`GetSnapshot`](phase09_txn/manager.go#L144) | 🟡 Medium | Take an MVCC snapshot of which transactions are running, as seen by transaction myXid. | [3 tests](phase09_txn/manager_test.go#L188) |
| 9.7 | [`XidInSnapshot`](phase09_txn/manager.go#L173) | 🟡 Medium | Report whether xid must be treated as still running by someone using snapshot snap. | [4 tests](phase09_txn/manager_test.go#L228) |
| 9.8 | [`TupleVisible`](phase09_txn/heap.go#L94) | 🔴 Hard | Decide whether tuple version v is visible to a statement using snapshot snap, setting hint bits along the way. | [3 tests](phase09_txn/heap_test.go#L27) |
| 9.9 | [`Heap.Insert`](phase09_txn/heap.go#L141) | 🟢 Easy | Append a new tuple version created by transaction xid in command cid and return its TID. | [1 test](phase09_txn/heap_test.go#L138) |
| 9.10 | [`Heap.Delete`](phase09_txn/heap.go#L181) | 🟡 Medium | Mark the version at tid as deleted by transaction xid in command cid, or report why it can't be. | [2 tests](phase09_txn/heap_test.go#L173) |
| 9.11 | [`Heap.Update`](phase09_txn/heap.go#L221) | 🟡 Medium | Replace the version at tid with a new version holding row: stamp the old one, append the new one, link them by ctid. | [3 tests](phase09_txn/heap_test.go#L239) |
| 9.12 | [`Heap.Scan`](phase09_txn/heap.go#L247) | 🟢 Easy | Sequential scan: return the TIDs of every version visible to snap, in heap order. | [4 tests](phase09_txn/heap_test.go#L326) |
| 9.13 | [`StatementSnapshot`](phase09_txn/txn.go#L126) | 🟡 Medium | Start a new statement: assign its command id and return the snapshot it must use, according to the isolation level. | [7 tests](phase09_txn/txn_test.go#L38) |
| 9.14 | [`UpdateWhere`](phase09_txn/txn.go#L153) | 🔴 Hard | Run "UPDATE t SET row = set(row) WHERE pred(row)" as one statement, handling concurrent updates per isolation level. | [8 tests](phase09_txn/txn_test.go#L176) |
| 9.15 | [`RollbackTo`](phase09_txn/txn.go#L195) | 🔴 Hard | Run "ROLLBACK TO SAVEPOINT name": undo (by aborting xids) everything done since the savepoint, then re-open it. | [7 tests](phase09_txn/txn_test.go#L377) |
| 9.16 | [`LockConflicts`](phase09_txn/lock.go#L76) | 🟢 Easy | Report whether lock modes a and b conflict (cannot be held at once by two different transactions). | [2 tests](phase09_txn/lock_test.go#L11) |
| 9.17 | [`Acquire`](phase09_txn/lock.go#L113) | 🟡 Medium | Request lock mode on tag for transaction xid: grant it now or append it to the FIFO wait queue. | [3 tests](phase09_txn/lock_test.go#L91) |
| 9.18 | [`Release`](phase09_txn/lock.go#L146) | 🔴 Hard | Drop everything xid has on tag (granted modes and a queued request) and wake up the waiters that can now run. | [5 tests](phase09_txn/lock_test.go#L213) |
| 9.19 | [`DetectDeadlock`](phase09_txn/lock.go#L174) | 🟡 Medium | Build the wait-for graph of the lock table, find every transaction stuck on a cycle, and pick the youngest as the victim. | [2 tests](phase09_txn/lock_test.go#L297) |
| 9.20 | [`Vacuum`](phase09_txn/vacuum.go#L5) | 🟡 Medium | Remove tuple versions that no transaction can ever see again, set hint bits on the survivors, and return how many were removed. | [5 tests](phase09_txn/vacuum_test.go#L10) |
| 9.21 | [`Freeze`](phase09_txn/vacuum.go#L47) | 🟡 Medium | Freeze committed xmins older than cutoff so they survive xid wraparound; return how many versions were frozen. | [3 tests](phase09_txn/vacuum_test.go#L131) |

<a id="phase-10"></a>

### Phase 10 — Write-Ahead Log & Crash Recovery

Make the database survive a power failure: encode checksummed WAL records, append them to a log buffer, read the log back and stop cleanly at a torn tail, flush it with group commit, enforce the write-ahead rule in the buffer pool, protect against torn pages with full-page images, take fuzzy checkpoints, and rebuild the exact committed state after a crash with the three ARIES passes (analysis, redo, undo with compensation log records).

📁 [`phase10_wal/`](phase10_wal/) · 📖 [phase notes](phase10_wal/README.md) · ▶️ `make phase P=10`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 10.1 | [`EncodeRecord`](phase10_wal/record.go#L3) | 🟢 Easy | Serialise a record into its on-disk bytes: 29-byte header + body, with a CRC-32C over everything after the CRC field. | [2 tests](phase10_wal/record_test.go#L11) |
| 10.2 | [`DecodeRecord`](phase10_wal/record.go#L38) | 🟡 Medium | Parse and verify the record at the start of buf; return it and its length, or say why the bytes are not a valid record. | [3 tests](phase10_wal/record_test.go#L68) |
| 10.3 | [`Append`](phase10_wal/log.go#L117) | 🟢 Easy | Append a record to the in-memory log buffer and return its LSN (= the byte offset where it starts). | [3 tests](phase10_wal/log_test.go#L11) |
| 10.4 | [`ReadRecords`](phase10_wal/reader.go#L5) | 🟡 Medium | Decode every valid record from LSN `from` onward and return them plus the end of the valid log, stopping cleanly at a torn tail. | [3 tests](phase10_wal/reader_test.go#L10) |
| 10.5 | [`FlushUpTo`](phase10_wal/flush.go#L5) | 🔴 Hard | Make the record at `lsn` (and everything before it) durable, sharing one fsync among all concurrent callers (group commit). | [3 tests](phase10_wal/flush_test.go#L12) |
| 10.6 | [`FlushPage`](phase10_wal/bufferpool.go#L66) | 🟡 Medium | Write a dirty page to disk, obeying the write-ahead rule: the WAL up to the page's LSN must be durable first. | [4 tests](phase10_wal/bufferpool_test.go#L23) |
| 10.7 | [`Update`](phase10_wal/db.go#L67) | 🟡 Medium | Overwrite bytes [off, off+len(data)) of a page on behalf of txn: log first (a full-page image if the page was clean, then an UPDATE record), then change the page. | [3 tests](phase10_wal/db_test.go#L13) |
| 10.8 | [`Commit`](phase10_wal/db.go#L112) | 🟡 Medium | Commit txn: append a COMMIT record, remove txn from the ATT, and return only once the commit record is durable. | [3 tests](phase10_wal/db_test.go#L136) |
| 10.9 | [`RedoRecord`](phase10_wal/redo.go#L5) | 🟡 Medium | Re-apply one logged change to a page, but only if the page does not already contain it (page LSN test); full-page images are restored unconditionally. | [4 tests](phase10_wal/redo_test.go#L24) |
| 10.10 | [`Checkpoint`](phase10_wal/db.go#L144) | 🟢 Easy | Take a fuzzy checkpoint: log a snapshot of the ATT and DPT, make it durable, then point the master record at it. | [3 tests](phase10_wal/db_test.go#L224) |
| 10.11 | [`Analyze`](phase10_wal/analysis.go#L14) | 🔴 Hard | ARIES analysis pass: starting from the last checkpoint, rebuild the Active Transaction Table and the Dirty Page Table as of the crash. | [5 tests](phase10_wal/analysis_test.go#L27) |
| 10.12 | [`RedoPass`](phase10_wal/redo.go#L40) | 🟡 Medium | Repeat history: replay every page-changing record from `from` to the end of the valid log, filtered by the dirty page table. | [3 tests](phase10_wal/redo_test.go#L127) |
| 10.13 | [`Undo`](phase10_wal/undo.go#L5) | 🔴 Hard | Roll back transactions by walking their PrevLSN chains backwards, writing a compensation log record (CLR) for every change undone, and finish each with an ABORT record. | [5 tests](phase10_wal/undo_test.go#L55) |
| 10.14 | [`Recover`](phase10_wal/recover.go#L3) | 🔴 Hard | Bring a freshly opened DB back to a consistent state after a crash: analysis, redo, undo — committed changes present, uncommitted ones gone. | [6 tests](phase10_wal/recover_test.go#L22) |

<a id="phase-11"></a>

### Phase 11 — Engine: one database out of ten phases

The capstone: glue phases 1–10 into a small but real Postgres-like database with a Go API — tables stored as files of 8 KB slotted pages behind buffer pools, rows encoded as heap tuples with `xmin`/`xmax`, MVCC snapshots and a commit log, B+Tree indexes with INCLUDE columns, a planner that picks Seq / Index / Index Only scans and hash joins and stacks sort, aggregation and limit operators on top, UPDATE / DELETE as new tuple versions, VACUUM, checkpoints, and crash recovery that replays the write-ahead log. Every problem is a piece of glue: the place where you decide how two subsystems meet.

📁 [`phase11_engine/`](phase11_engine/) · 📖 [phase notes](phase11_engine/README.md) · ▶️ `make phase P=11`

| # | Problem | Level | What you build | Tests |
|---|---|---|---|---|
| 11.1 | [`WriteFileAtomic`](phase11_engine/catalog.go#L99) | 🟢 Easy | Replace the file at path with data so that a crash leaves either the complete old file or the complete new one. | [4 tests](phase11_engine/catalog_test.go#L13) |
| 11.2 | [`Visible`](phase11_engine/txn.go#L13) | 🟢 Easy | Decide whether a stored tuple version is visible to a snapshot by adapting its on-disk header to phase 9's TupleVisible. | [2 tests](phase11_engine/visible_test.go#L13) |
| 11.3 | [`Txn.Snapshot`](phase11_engine/txn.go#L52) | 🟡 Medium | Return the MVCC snapshot the transaction's next statement must use: a fresh one per statement under READ COMMITTED, one per transaction under REPEATABLE READ. | [3 tests](phase11_engine/visible_test.go#L61) |
| 11.4 | [`TableHeap.Insert`](phase11_engine/storage.go#L234) | 🟡 Medium | Store a new tuple version stamped with xmin = xid: form it, find a page with room, insert it, WAL-log it, return its TID. | [3 tests](phase11_engine/heap_test.go#L22) |
| 11.5 | [`SeqScan.Next`](phase11_engine/scan.go#L37) | 🟡 Medium | Return the next visible row of the table, reading it one page at a time through the buffer pool. | [3 tests](phase11_engine/heap_test.go#L118) |
| 11.6 | [`Txn.Commit`](phase11_engine/txn.go#L84) | 🟡 Medium | Make the transaction's changes durable and visible: WAL commit record, flush, then the clog bit. | [3 tests](phase11_engine/commit_test.go#L13) |
| 11.7 | [`Txn.Rollback`](phase11_engine/txn.go#L118) | 🟢 Easy | Abort the transaction: mark its xid aborted and leave every page untouched. | [2 tests](phase11_engine/commit_test.go#L95) |
| 11.8 | [`Index.Entry`](phase11_engine/index.go#L80) | 🟢 Easy | Build the B+Tree leaf entry for one heap row: memcomparable key, heap TID, and the covering payload. | [2 tests](phase11_engine/index_test.go#L18) |
| 11.9 | [`Index.Build`](phase11_engine/index.go#L107) | 🟡 Medium | Build the index from every tuple version already in the heap, the way CREATE INDEX does: collect, sort, bulk-load. | [3 tests](phase11_engine/index_test.go#L92) |
| 11.10 | [`Txn.Insert`](phase11_engine/txn.go#L146) | 🟡 Medium | INSERT one row: coerce it to the table's types, store it in the heap, and add an entry to every index of the table. | [3 tests](phase11_engine/index_test.go#L170) |
| 11.11 | [`KeyBounds`](phase11_engine/index.go#L143) | 🟡 Medium | Turn "leading index column <op> v" into the B+Tree range [lo, hi] over encoded keys that holds exactly the matching entries. | [3 tests](phase11_engine/index_test.go#L230) |
| 11.12 | [`IndexScan.Next`](phase11_engine/index.go#L211) | 🟡 Medium | Return the next visible heap row whose index key is in range: walk the B+Tree cursor, fetch each TID from the heap, recheck visibility. | [3 tests](phase11_engine/index_test.go#L307) |
| 11.13 | [`IndexOnlyScan.Next`](phase11_engine/index.go#L275) | 🟡 Medium | Return the next visible entry's payload, skipping the heap for pages the visibility map marks all-visible. | [3 tests](phase11_engine/index_test.go#L387) |
| 11.14 | [`Txn.PlanScan`](phase11_engine/planner.go#L15) | 🔴 Hard | Choose the access path for one table — Seq Scan, Index Scan or Index Only Scan — and put the remaining conditions in a Filter. | [4 tests](phase11_engine/planner_test.go#L65) |
| 11.15 | [`PlanJoin`](phase11_engine/planner.go#L69) | 🟡 Medium | Join two plans ON left.leftCol = right.rightCol, choosing the phase-8 algorithm by join type. | [3 tests](phase11_engine/planner_test.go#L212) |
| 11.16 | [`Txn.PlanQuery`](phase11_engine/planner.go#L116) | 🔴 Hard | Plan a whole SELECT: base scan, joins, aggregation, sort, limit and projection, stacked into one iterator tree. | [4 tests](phase11_engine/planner_test.go#L295) |
| 11.17 | [`TableHeap.Delete`](phase11_engine/mvcc.go#L7) | 🔴 Hard | MVCC delete of one tuple version: check who else touched it, then stamp xmax = my xid in place and WAL-log it. | [3 tests](phase11_engine/dml_test.go#L45) |
| 11.18 | [`Txn.Update`](phase11_engine/txn.go#L228) | 🟡 Medium | UPDATE table SET col = value, ... WHERE ...: for every target row, delete the old version and insert the new one. | [4 tests](phase11_engine/dml_test.go#L143) |
| 11.19 | [`DB.Vacuum`](phase11_engine/vacuum.go#L11) | 🔴 Hard | VACUUM one table: remove versions no snapshot can ever see, together with their index entries, compact the pages, and refresh the FSM and visibility map. | [3 tests](phase11_engine/vacuum_test.go#L35) |
| 11.20 | [`DB.Checkpoint`](phase11_engine/recovery.go#L16) | 🟡 Medium | Make everything up to now durable in the data files, so crash recovery can start after this point. | [3 tests](phase11_engine/recovery_test.go#L27) |
| 11.21 | [`DB.Recover`](phase11_engine/recovery.go#L53) | 🔴 Hard | Crash recovery: replay the WAL tail onto the data files (redo), and rebuild the commit status of the transactions it mentions. | [6 tests](phase11_engine/recovery_test.go#L107) |
