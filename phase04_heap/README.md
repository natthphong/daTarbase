# Phase 4 — Heap File & Table Scans

Build a table's heap file — an unordered bag of pages holding tuples — and every way Postgres reads it: a free space map (flat, then as a real max-tree) that tells inserts where room is, insert / fetch / delete / update with HOT-like same-page updates and ctid forwarding chains, then the scan family: sequential, synchronized (wrap-around), TID range, bitmap heap scan and TABLESAMPLE SYSTEM, plus TOAST for values too big to fit in a page.

## Concepts

### 1. The heap is just pages

```
 relation file:   [ page 0 ][ page 1 ][ page 2 ] … [ page N-1 ]
 tuple address:   TID = (page, slot)   aka ctid, ItemPointer
```

- A Postgres table ("heap") has **no order**. A tuple goes wherever there is room;
  its physical address `ctid = (block, offset)` is what indexes store.
- This phase uses a simplified `MemPage` (Go slices instead of bytes; phase 2 is the
  byte-level slotted page). Each tuple costs `len + 4` bytes (the 4 is the line
  pointer), out of `8192 - 8 = 8184` bytes.
- **Nothing is ever removed in place.** DELETE stamps `xmax`; UPDATE writes a new
  version. Dead versions keep their bytes until VACUUM prunes them. Here, "dead" is
  a flag and `Used` never decreases.
- `PageStore.Reads` counts `ReadPage` calls: it stands in for `ReadBuffer` and lets
  the tests check I/O behaviour of each access method.

### 2. Free Space Map (FSM)

```
 heap pages:        0      1      2      3
 free bytes:        20    100     40    300
 category:           0      3      1      9        cat = free / 32  (round DOWN)
 need 97 bytes →  cat ceil(97/32) = 4  → page 3     need is rounded UP
```

- Reading every page to find room would make INSERT O(table). Postgres keeps a
  separate fork (`<relfilenode>_fsm`) with **one byte per heap page**: a category
  `free / (BLCKSZ/256)`. Rounding free DOWN and need UP means the FSM can miss an
  exact fit but never sends you to a page that is too small (`fsm_space_avail_to_cat`,
  `fsm_space_needed_to_cat`).
- The FSM is a **hint**, not the truth: it is not WAL-logged precisely and may be
  stale. `RelationGetBufferForTuple` re-checks the real page and, if it lied, calls
  `RecordAndGetPageWithFreeSpace` to correct the entry and ask again. If nothing has
  room, the relation is **extended** with a new page.
- Inside each FSM page the categories are arranged as a **binary max-tree**
  (`fsmpage.c`): leaves are heap pages, each inner node is the max of its children.
  The root answers "is there any page with ≥ N bytes?" in O(1), and descending finds
  one in O(log n). Upper FSM levels form the same kind of tree over FSM pages, so the
  whole map is a tree of trees.
- `fp_next_slot` makes searches start where the previous one ended, so concurrent
  backends spread out over different pages instead of all fighting for page 0.

### 3. Updates, HOT and ctid chains

```
 page 0: [ (0,0) v1 dead ─ctid→ (0,2) ] [ (0,1) other ] [ (0,2) v2 dead ─ctid→ (1,0) ]
 page 1: [ (1,0) v3 live ]
```

- `heap_update` writes the new version and sets the old version's `t_ctid` to point at
  it. Readers (and `SELECT … FOR UPDATE`, `heap_get_latest_tid`) follow the chain to
  the newest version.
- If the new version lands on the **same page** and no indexed column changed, it's a
  **Heap-Only Tuple** (HOT) update: no new index entries are made; the index still
  points at the chain root and `heap_hot_search_buffer` walks the chain inside the
  page. Leaving free space on pages (`fillfactor < 100`) makes HOT more likely.
- Otherwise the new version goes wherever the FSM says, and every index gets a new
  entry pointing at it.

### 4. The scan family

| Scan | How it reads the heap | Postgres |
|---|---|---|
| Seq Scan | page 0 → N-1, every slot, skip invisible | `heapgettup_pagemode` |
| Synchronized seq scan | starts where another scan currently is, wraps around | `syncscan.c` (`synchronize_seqscans`, tables > ¼ shared_buffers) |
| TID Range Scan | only pages `lo.page … hi.page` for `WHERE ctid BETWEEN` | `heap_set_tidrange` (PG 14) |
| Bitmap Heap Scan | index TIDs → bitmap sorted by page → each page once, in order | `tidbitmap.c`, `nodeBitmapHeapscan.c` |
| TABLESAMPLE SYSTEM | one coin per **page**, read only the winners | `tsm_system.c` |

- A sequential scan reads `rs_nblocks` fixed at scan start: pages added later belong
  to transactions the scan's snapshot can't see anyway.
- **Synchronized scans**: when two big scans run at once, the second joins the first
  (`ss_get_location`) so one physical read serves both; it then wraps to page 0 to
  pick up what it missed. Every scan reports its position (`ss_report_location`).
  That's why unordered `SELECT * FROM big` can return rows in a different order each run.
- **Bitmap heap scan**: a plain index scan follows index order and may hit the same
  heap page many times at random. The bitmap scan collects all TIDs first (a
  `TIDBitmap`, one entry per page), then reads pages in physical order, each once —
  sequential-ish I/O with prefetching. When the bitmap exceeds `work_mem` some pages
  become **lossy** (page-only, no slot list): every tuple on them is returned and the
  qual is **rechecked** (`Recheck Cond` in EXPLAIN).
- **TABLESAMPLE SYSTEM (p)** picks whole blocks (fast, clustered sample);
  **BERNOULLI (p)** flips a coin per row (reads everything). `REPEATABLE (seed)` makes
  the choice deterministic.

### 5. TOAST

```
 main tuple:   [ id | name | 0x01 · value_id · raw_size ]          ← 9-byte pointer here
 pg_toast_N:   (chunk_id=1, seq=0, 1996 bytes) (1, 1, 1996) (1, 2, 1008)
 index on (chunk_id, chunk_seq) finds the chunks in order
```

- A tuple must fit in a page, so when a row would exceed `TOAST_TUPLE_THRESHOLD`
  (~2 KB) Postgres first tries **compression** (pglz/lz4), then moves the largest
  varlena columns **out of line** into the table's TOAST relation, cut into chunks of
  `TOAST_MAX_CHUNK_SIZE` (1996 bytes on 8 KB pages).
- The main tuple keeps a `varatt_external` pointer: raw size, stored size, value id
  (an OID), toast relation id. **Detoasting** fetches the chunks via the toast index
  in sequence order and verifies the sequence — a missing or out-of-order chunk is the
  famous `missing chunk number 0 for toast value …` corruption error.
- TOAST is lazy: a query that never touches the column never reads the chunks, and an
  UPDATE that doesn't change a toasted column just copies the pointer.

## Prerequisites

None — this phase is self-contained (only `pkg/types`). The page abstraction
(`MemPage`, `PageStore`) is provided in `page.go`.

## Suggested order

1. **4.1–4.2** flat FSM: categories and first-fit search.
2. **4.3–4.4** FSM as a max-tree (`fsm_set_avail`, `fsm_search_avail`).
3. **4.5–4.8** heap operations: Insert (FSM + extend), Fetch (follow ctid chains),
   Delete, Update (HOT vs. cold).
4. **4.9–4.12** scans: sequential, synchronized, TID range, bitmap heap scan.
5. **4.13–4.14** TOAST and detoast.
6. **4.15** TABLESAMPLE SYSTEM.
