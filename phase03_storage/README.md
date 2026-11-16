# Phase 3 — Disk Manager & Buffer Pool

Build the bottom of the database: a disk manager that reads and writes 8 KB pages in relation files (including Postgres-style 1 GB segment files), and a buffer pool that caches those pages in a fixed set of memory frames, with pinning, dirty tracking, write-back, three replacement policies (LRU, Postgres's clock sweep, LRU-K) and a ring-buffer strategy that stops big sequential scans from wiping the cache.

## Concepts

### 1. Storage manager: pages in files

```
 SELECT … → executor → heap/index AM → ReadBuffer(rel, blkno) → bufmgr → smgr (md.c) → kernel
```

- Every table and index is a **relation**: an array of 8 KB **blocks** (`BLCKSZ`,
  compile-time constant). Block `n` lives at byte offset `n * 8192`. There is no
  per-page directory: the page number *is* the address.
- The **storage manager** (`smgr.c`) is a thin switch in front of `md.c`
  ("magnetic disk"): `mdread`, `mdwrite`, `mdextend`, `mdnblocks`, `mdtruncate`.
  This phase's `DiskManager` interface is exactly that API.
- **Reading past the end is an error**, not zeros: `mdread` fails with
  "could not read block N … read only 0 of 8192 bytes". The number of blocks is
  derived from the file size (`mdnblocks` = size / BLCKSZ).
- **Short writes**: `pwrite` may write fewer bytes than asked. Postgres
  reports "could not write block N: wrote only X of 8192 bytes" (`mdwrite`), and
  its WAL code uses `pg_pwrite_with_retry`-style loops. Problem 3.2 makes you
  write the retry loop and detect "no progress".
- **Extending** (`mdextend`) writes a new zero page at the end. Writing a page
  that was never extended is a bug, so `WritePage` refuses ids `>= NumPages`.
- Block numbers are 32-bit; `InvalidBlockNumber` = `0xFFFFFFFF` is reserved,
  so a relation holds at most 2³²−1 blocks (32 TB at 8 KB).

### 2. Segment files

```
 base/16384/24576      blocks       0 … 131071      (1 GB)
 base/16384/24576.1    blocks  131072 … 262143      (1 GB)
 base/16384/24576.2    blocks  262144 …             (partial)
```

- No file grows past `RELSEG_SIZE` blocks (131072 × 8 KB = 1 GB), a choice from
  the days when filesystems couldn't handle large files. It's still useful:
  truncation and unlinking work per segment, and per-file operations stay bounded.
- Block `b` → segment `b / RELSEG_SIZE`, local block `b % RELSEG_SIZE`
  (`_mdfd_getseg`). Every segment except the last is exactly full; the next
  segment file is created **lazily** when the last one fills up (`mdextend`).
- At startup Postgres opens `rel`, `rel.1`, … until a file is missing — the
  provided `OpenSegmentedDisk` does the same.

### 3. Shared buffers

```
 BufferDesc[NBuffers]          Block[NBuffers] (8 KB each)        buffer mapping hash table
 +-----+----------------+      +--------+                         (relfilenode, fork, blk) -> buf_id
 | tag | state:          |     | page   |                         128 partitions, each with its own LWLock
 |     |  refcount (pin) |---->| bytes  |
 |     |  usage_count    |     +--------+
 |     |  DIRTY, VALID…  |
 +-----+----------------+
```

- `shared_buffers` is one big array of 8 KB frames plus a `BufferDesc` per frame.
  A **hash table** maps a page's tag to the frame holding it (our `pageTable`).
- **Pinning** (`PinBuffer` / `UnpinBuffer`): a backend increments `refcount` while it
  uses a page. A pinned buffer can never be evicted — this is how a frame's
  bytes stay valid while you read them. Forgetting to unpin leaks a frame forever
  (Postgres warns "buffer refcount leak" at transaction end).
- **Dirty bit** (`MarkBufferDirty`): set when a page is modified; it is never
  cleared by the modifier — only a successful write (`FlushBuffer`) clears it.
  That's why `UnpinPage(id, false)` must not clear it.
- **Miss path** (`BufferAlloc`): pick a victim, if dirty write it out first
  (after flushing WAL up to the page's LSN — the write-ahead rule), remove
  its old mapping, insert the new mapping, read the block. Postgres does the I/O
  outside the mapping lock and uses per-buffer I/O locks so other backends
  waiting for the same page sleep instead of double-reading. We simplify with
  one mutex around everything — correct, not scalable.
- **Double buffering**: Postgres uses buffered I/O, so pages also sit in the OS
  page cache. That's why `shared_buffers` is typically ~25% of RAM, not 90%.
- **Writers**: backends rarely write their own victims in a healthy system. The
  **background writer** cleans buffers ahead of the clock hand, and the
  **checkpointer** writes *all* dirty buffers (`BufferSync`), sorted by
  file and block for sequential I/O — our `FlushAll` sorts by page id for the
  same reason.

### 4. Replacement policies

- **LRU** (problems 3.7–3.8): doubly-linked list + hash map, O(1) access.
  Simple, but every access must move a list node under a lock — a global
  contention point — and one big scan pushes out everything ("sequential flooding").
- **Clock sweep** (problem 3.9, Postgres since 8.1): each buffer has a
  `usage_count` (0–5, `BM_MAX_USAGE_COUNT`). Pinning increments it; the hand
  (`nextVictimBuffer`, advanced atomically by `ClockSweepTick`) walks the array,
  decrementing the count of every *unpinned* buffer it passes, and takes the
  first unpinned buffer whose count is already 0. Accesses only touch the buffer's
  own state word — no global list to reorder. The cap of 5 means a once-hot page
  that stopped being used survives at most 5 sweeps.
- **LRU-K** (problem 3.10, O'Neil/O'Neil/Weikum 1993): evict the page whose K-th
  most recent access is oldest; pages with fewer than K accesses are evicted
  first. A page touched once by a scan can't push out a page touched twice.
  Postgres history: 8.0 shipped **ARC**, which was replaced by **2Q** in 8.0.2
  because of an IBM patent, and both were replaced by the clock sweep in 8.1
  for its lock-free scalability.

### 5. Buffer access strategies (rings)

- A `SELECT * FROM huge_table` would otherwise cycle every page through shared
  buffers and evict the whole working set. Postgres gives large sequential
  scans (`BAS_BULKREAD`, tables > ¼ of shared_buffers), `VACUUM` (`BAS_VACUUM`)
  and `COPY`/`CREATE TABLE AS` (`BAS_BULKWRITE`, 16 MB) a small **ring** of
  buffers (256 KB = 32 buffers for bulk reads).
- `GetBufferFromRing`: try the buffer in the next ring slot; if nobody else has
  it pinned (and its usage_count ≤ 1) reuse it, otherwise fall back to the normal
  clock sweep and remember the new buffer in that slot (`AddBufferToRing`).
  Hits on pages already in shared buffers don't involve the ring at all.
- Bulk *writes* also reject ring buffers whose reuse would need a WAL flush
  (`StrategyRejectBuffer`) — we simply write them back.
- Side effect you can observe in Postgres: two consecutive `seq scan`s of a big
  table both read from disk (or the OS cache), because the first scan only left
  32 of its pages in shared buffers. `synchronize_seqscans` lets concurrent scans
  share the ring's position to soften this.

## Prerequisites

None — this phase only imports `pkg/types`. `MemDisk` (provided) is an
instrumented in-memory `DiskManager`; buffer-pool tests use it to assert exact
disk reads and writes, and a small test-only LRU so they don't depend on your
replacer solutions.

## Suggested order

1. **3.1–3.3** `FileDisk`: read, write with short-write retry, extend.
2. **3.4–3.6** segments: arithmetic, locate, lazy segment creation.
3. **3.7–3.8** LRU replacer, **3.9** clock sweep, **3.10** LRU-K.
4. **3.11** `UnpinPage`, **3.12** `acquireFrame` (free list → victim → write-back),
   **3.13** `FetchPage` (hit/miss, concurrency), **3.14** `NewPage`.
5. **3.15–3.16** flushing, then **3.17** the ring strategy.

Run one problem's tests with `go test -run '^TestP3_13_' ./phase03_storage/`,
and always run the buffer pool with `-race`.
