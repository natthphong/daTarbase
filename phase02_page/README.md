# Phase 2 — Slotted Page

Build the 8 KB slotted page that every Postgres table and index is made of: a small header, an array of line pointers growing up from the front, tuple bytes growing down from the back, and the free hole in between. You will encode line pointers with Postgres' `LP_UNUSED / LP_NORMAL / LP_DEAD` states, insert (recycling free slots), read, scan, delete, update in place, defragment without changing any tuple's slot number, honour a table's fillfactor, checksum the page, load it back from disk defensively, and dump it like `pageinspect`.

## Concepts

### 1. The page layout (`bufpage.h`)

```
 0        lower                                    upper                8192
 +--------+------+------+------+----------------------+--------+--------+
 | header | lp 0 | lp 1 | lp 2 |      free hole       | tup 2  | tup 0  |   (tup 1 deleted
 +--------+------+------+------+----------------------+--------+--------+    and compacted)
           line pointers grow →                  ← tuple data grows
```

- Everything is a page: heap tables, B-tree, hash and GIN indexes, the FSM and
  visibility map. All share `PageHeaderData` and the slotted layout.
- Postgres' header is 24 bytes: `pd_lsn` (8, WAL position of the last change —
  see the WAL phase), `pd_checksum` (2), `pd_flags` (2), `pd_lower` (2),
  `pd_upper` (2), `pd_special` (2, index-AM private area at the end of the page),
  `pd_pagesize_version` (2) and `pd_prune_xid` (4). We keep only the four fields
  that matter here: `lower | upper | flags | checksum` = 8 bytes.
- `pd_lower` = end of the line-pointer array, `pd_upper` = start of tuple data.
  Free space is always the single hole `[lower, upper)`. A page is full when
  the two meet.
- Tuples are placed from the END backwards, so the two arrays can grow toward
  each other without either one knowing how many items will eventually fit.

### 2. Line pointers (`ItemIdData`, `itemid.h`)

```
 Postgres (32 bits):   lp_off:15 | lp_flags:2 | lp_len:15
 This phase:           off uint16 | flags<<14 | len (14 bits)       (4 bytes, LE)
```

| state       | meaning                                                             |
|-------------|---------------------------------------------------------------------|
| LP_UNUSED 0 | free slot, may be handed out by the next insert; off = len = 0      |
| LP_NORMAL 1 | points at a live tuple                                              |
| LP_REDIRECT 2 | HOT chain: points at another slot (heap-only tuples, later phase) |
| LP_DEAD 3   | tuple is dead; slot number still reserved (indexes may point at it) |

- A **TID / ctid** is `(block, offset number)`: it names a *line pointer*, not
  a byte offset. That one level of indirection is what lets Postgres move
  tuples inside a page (compaction) without touching any index.
- Postgres' OffsetNumber starts at 1; we use 0-based slots.

### 3. Inserting: `PageAddItemExtended`

- Look for an `LP_UNUSED` slot first (Postgres only bothers if the page has the
  `PD_HAS_FREE_LINES` hint set); otherwise append a new line pointer at `lower`.
- Copy the tuple to `upper - len` (Postgres also `MAXALIGN`s it to 8 bytes; we
  skip alignment), bump `upper` down and `lower` up.
- `PageGetFreeSpace` subtracts one line pointer from the hole — the space a new
  item would really get. Our `FreeSpace` only does so when no slot can be recycled.

### 4. Deleting, pruning, vacuuming

- A Postgres `DELETE` does **not** touch the line pointer: it stamps `xmax` in
  the tuple header (MVCC, later phase). Only when the tuple is dead to every
  snapshot does pruning (`heap_page_prune`, run opportunistically on reads, or
  VACUUM) mark it `LP_DEAD` and later `LP_UNUSED`.
- Our `DeleteTuple` jumps straight to `LP_DEAD`, keeping off/len. The bytes stay
  until compaction — exactly like a dead-but-not-yet-vacuumed slot.
- A slot can only become `LP_UNUSED` once **no index entry points at it**:
  VACUUM first removes the index entries, then (`lazy_vacuum_heap_page`) turns
  `LP_DEAD` into `LP_UNUSED`. That is why `InsertTuple` must never recycle a dead slot.

### 5. Defragmenting: `PageRepairFragmentation` + `compactify_tuples`

```
 before: | hdr | N | D | N | D |      hole      |ddd|cc|bbbbbbbb|aaaa|
 after:  | hdr | N | U | N |         hole            |cc|aaaa|
```

- Collect the live items, sort, and copy them to the end of the page so the
  hole is contiguous again. Each item's `lp_off` is updated; slot numbers never change.
- Overlapping moves are the trap: a tuple's new home may overlap a tuple that
  hasn't moved yet. `compactify_tuples` has a fast path that `memmove`s when the
  items are already in reverse-offset order and otherwise copies into a scratch
  buffer first.
- Postgres 14 added `PageTruncateLinePointerArray`: trailing `LP_UNUSED` slots
  are cut off so `lower` shrinks too.

### 6. Fillfactor and HOT

- `CREATE TABLE t (...) WITH (fillfactor = 90)` makes `INSERT`s stop using a
  page once it is 90% full (`RelationGetTargetPageFreeSpace`, checked in
  `RelationGetBufferForTuple`).
- The reserved 10% is for UPDATEs: if the new version fits on the same page and
  no indexed column changed, Postgres does a **HOT** (heap-only tuple) update —
  no new index entries, and the old version can be pruned with only the page lock.
- An empty page always accepts a tuple, whatever the fillfactor.

### 7. Checksums and torn pages

- With `initdb --data-checksums` (default since PG 18), `PageSetChecksumCopy`
  stamps a 16-bit checksum on a *copy* of the page just before `write()`, and
  `PageIsVerifiedExtended` verifies it after every `read()`.
- The algorithm (`checksum_impl.h`) is a vectorised FNV-1a over the page with
  `pd_checksum` treated as 0; the block number is XOR-ed in so a correct page
  written to the wrong place is still caught; the result is folded with
  `% 65535 + 1`, so 0 never appears (0 = "no checksum").
- An all-zero page is valid (the relation was extended but the page never
  written, e.g. crash right after `mdextend`).
- A checksum catches torn writes (half an 8 KB page reached disk), but WAL's
  **full-page writes** are what repair them during recovery.
- Header sanity (`PageHeaderIsValid`): `pd_lower >= SizeOfPageHeaderData`,
  `pd_lower <= pd_upper <= pd_special <= BLCKSZ`, no unknown flag bits.

### 8. Looking inside: `pageinspect`

```sql
SELECT lp, lp_off, lp_flags, lp_len, t_xmin, t_xmax, t_ctid
FROM heap_page_items(get_raw_page('accounts', 0));
```

`page_header()` shows `lower`, `upper`, `special`, `checksum`. `Dump()` is a tiny
version of these two functions.

## Prerequisites

None — only `pkg/types` (`PageSize`, `PageID`, `SlotID`).

## Suggested order

1. **2.1–2.2** encode/decode line pointers — everything else is built on them.
2. **2.3–2.6** free space, read, insert (with slot recycling), scan.
3. **2.7–2.8** delete and in-place update — they create the fragmentation…
4. **2.9** …that `Compact` repairs (the hard one: think about overlapping copies).
5. **2.10** fillfactor-aware insert.
6. **2.11–2.14** checksum, verify, serialize and defensive load.
7. **2.15** `Dump` — handy to debug everything above.
