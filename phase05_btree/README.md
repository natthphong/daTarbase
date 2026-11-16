# Phase 5 — B+Tree Index

Build the index structure behind almost every Postgres index: a B+Tree (`nbtree`) over memcomparable byte keys that maps keys to heap TIDs. You implement search, inserts that split pages up to the root, range scans in both directions, prefix scans over composite keys, covering (INCLUDE) index-only scans, deletes that rebalance, `CREATE INDEX`-style bulk loading, a page format for nodes, and an `amcheck`-style validator. This is how `WHERE id = 42`, `ORDER BY ... LIMIT` and `WHERE a = 1 AND b > 5` avoid reading the whole table.

## Concepts

### Shape of a Postgres B-Tree

```
                         root (internal)                level 2
               [ -inf | c(3,1) | k(1,1) ]
              /            |             \
     [-inf | ...]     [-inf | ...]     [-inf | ...]     level 1
       /    \            /    \           /    \
   leaf <-> leaf <-> leaf <-> leaf <-> leaf <-> leaf     level 0  (entries: key -> TID)
```

- **Every entry lives in a leaf.** Internal pages only hold *pivot tuples* (separators) that
  steer the descent. Leaves are doubly linked (`btpo_prev` / `btpo_next` in the page's
  "special space", `BTPageOpaqueData`) so range scans just walk sideways.
- **Minus infinity.** The first pivot on an internal page has no key; it means "everything
  smaller than the next pivot". That is why an internal node with `k` separators has `k+1`
  downlinks — and why our `EncodePage` writes a key-less tuple 0.
- **High keys and right-links (Lehman & Yao).** Real Postgres pages also store a *high key*:
  an upper bound for the page. A scan that sees the high key can stop without reading the next
  page, and a concurrent reader that lands on a page that was just split notices "my key is
  above the high key" and follows the right-link. We skip high keys, so our range scans
  sometimes read one extra leaf to learn that the range ended — the read counters in the tests
  show this.
- **Heap TID as a key column (heapkeyspace, PG 12+).** Before v12, duplicates were stored in
  arbitrary order and finding the one index entry for a dead heap tuple meant scanning every
  duplicate. Since v12 the TID is the final tiebreaker column, so every tuple is unique and
  one descent finds it (`RemoveEntry`). Pivots may keep a TID when the key alone can't
  separate two pages — exactly what our separators do. PG 13 then added *deduplication*
  (posting lists: one key, many TIDs) to win back the space.
- **Suffix truncation.** When a leaf splits, Postgres copies up the *shortest* pivot that still
  separates the halves (dropping trailing key columns and the TID when possible). We copy up
  the full first key + TID of the right page.

### Insert and page splits

`_bt_doinsert` descends with `_bt_search`, remembering the path in a `BTStack` (our `[]Step`).
If the leaf is full, `_bt_split` moves part of the tuples to a new right sibling, fixes the
sibling links, and `_bt_insert_parent` inserts a downlink in the parent — which may split too,
all the way to `_bt_newlevel` that creates a new root. Leaf splits **copy** the separator up
(the entry stays in the leaf); internal splits **move** it up.

Where to split is chosen by `_bt_findsplitloc` (nbtsplitloc.c): roughly in the middle by
*space*, but when the page is the rightmost one it leaves the left page `fillfactor` full,
because ever-increasing keys (serials, timestamps) would otherwise leave every page half
empty. Our `TestP5_10_Insert_Shapes` shows the half-empty pages a naive middle split leaves.

### Unique indexes

`_bt_check_unique` looks for an equal key before inserting. Because of MVCC an equal key is
only a conflict if its heap tuple is live (or being inserted by an in-progress transaction,
in which case the inserter waits). We have no MVCC here, so every entry conflicts.

### Scans

`_bt_first` positions a scan: it builds an insertion scankey from the quals
(`a = 7 AND b >= 3` → descend to `(7, 3)`), descends, and binary-searches the leaf
(`_bt_binsrch`). `_bt_next` / `_bt_steppage` walk the leaf chain, forward or backward
(`ORDER BY ... DESC` and `max()` use backward scans). A multi-column index can serve any
**prefix** of its columns; with memcomparable keys a prefix of columns is literally a byte
prefix, and `LIKE 'abc%'` becomes the range `['abc', 'abd')` (`make_greater_string`).

### Index-only scans and INCLUDE

If every column a query needs is in the index, `IndexOnlyNext` can skip the heap — except
the index doesn't know whether a tuple is visible. It asks the **visibility map**: if the
heap page is all-visible, the entry is trusted; otherwise it fetches the heap tuple
(`Heap Fetches: N` in `EXPLAIN ANALYZE`). `INCLUDE (cols)` (PG 11) stores extra columns in
leaf tuples only — they are not part of the key, are never compared, and are truncated from
pivots — so an index can cover a query without widening its search key.

### Deletion

Postgres does **not** borrow or merge like the textbook. Entries are removed by VACUUM
(`btbulkdelete`) or opportunistically (simple / bottom-up deletion, PG 14), and only a
completely **empty** page is removed, in two stages (`_bt_pagedel`: mark half-dead, then
unlink from siblings and parent) so concurrent scans stay safe. Pages are never merged, which
is why an index can bloat and `REINDEX` exists. In this phase you implement the textbook
rebalance (borrow from a sibling, else merge, shrink the root) because it is the cleanest way
to learn the invariants.

### CREATE INDEX

`btbuild` feeds every heap tuple into a tuplesort, then `_bt_load` writes the sorted entries
into leaf pages left to right, filling each to `fillfactor` (default 90% for leaves, 70% for
internal pages), and pushes the first key of every new page into the level above
(`_bt_buildadd`). When the input ends, `_bt_uppershutdown` finishes each level and the last
single page becomes the root. Each page is written once and sequentially — far cheaper than
inserting one by one.

### Page format and checking

Index pages use the same slotted-page layout as heap pages (phase 2): header, line pointers,
tuples growing from the end, plus the special space. `contrib/amcheck` (`bt_index_check`)
verifies exactly the invariants of our `Validate`: keys ordered within pages, every key
within its parent's bounds, sibling links consistent, all leaves at the same level.

### Simplifications in this phase

- Nodes are capped by an item count (`MaxItems`), not by bytes; `EncodePage` shows the byte view.
- No high keys, no right-links, no locking: single-threaded.
- No suffix truncation, no deduplication, no MVCC.
- Textbook borrow/merge instead of empty-page deletion.

## Prerequisites

None of the earlier phases' code is imported (only `pkg/types`). Phase 1's memcomparable
encoding idea is reused: `EncodeInt` / `EncodeText` are provided here.

## Suggested order

1. **Search a node:** 5.1 `CompareItems` → 5.2 `LowerBound` → 5.3 `UpperBound`.
2. **Search the tree:** 5.4 `Descend` → 5.5 `Search`.
3. **Insert:** 5.6 `InsertItem` → 5.7 `SplitLeaf` → 5.8 `SplitInternal` → 5.9 `GrowRoot` → 5.10 `Insert` → 5.11 `InsertUnique`.
4. **Scans:** 5.12 `Next` (range scans, ASC/DESC) → 5.13 `Prefix` → 5.14 `IndexOnlyScan`.
5. **Delete:** 5.15 `RemoveEntry` → 5.16 `Borrow` → 5.17 `Merge` → 5.18 `Delete`.
6. **Build and store:** 5.19 `BulkLoad` → 5.20 `EncodePage` → 5.21 `DecodePage`.
7. **Check it all:** 5.22 `Validate` — its test runs a seeded 5000-operation workload against a sorted-slice oracle.

Tip: `NodeStore.Get` returns a copy. If you change a node, `Put` it back — the tests count
reads and writes, and an unsaved change simply doesn't exist.
