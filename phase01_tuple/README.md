# Phase 1 — Tuple Format & Data Types

How a row becomes bytes on disk, the way Postgres heap tuples do it: a fixed header, an optional null bitmap, and every non-NULL attribute aligned to its type — plus fast single-column access via cached offsets, an order-preserving key encoding for B+Trees, and "column tetris" to stop wasting bytes on padding.

## Concepts

### The heap tuple

Every row version Postgres stores is a *heap tuple*:

```
+-----------------------------+--------------+-----+------+-----+------+---
| HeapTupleHeaderData (23 B)  | null bitmap  | pad | att0 | pad | att1 | ...
+-----------------------------+--------------+-----+------+-----+------+---
0                             23             t_hoff (multiple of MAXALIGN = 8)
```

- **Header** (`htup_details.h`): `t_xmin`, `t_xmax` (inserting/deleting
  transaction — the heart of MVCC, phase on visibility), `t_cid`, `t_ctid`
  (pointer to the newer version), `t_infomask2` (low 11 bits = number of
  attributes), `t_infomask` (flags such as `HEAP_HASNULL`, `HEAP_HASVARWIDTH`,
  hint bits) and `t_hoff`, the offset of the user data.
- This phase uses a trimmed 13-byte header: `xmin u32 | xmax u32 | infomask u16
  | natts u16 | hoff u8`. Like Postgres, `hoff = MAXALIGN(header + bitmap)`.
  Postgres' 23-byte header leaves exactly one byte before 24, so tables with
  ≤ 8 columns get the null bitmap "for free"; ours fits up to 24 columns before
  hoff grows from 16 to 24.
- **Null bitmap**: present only if the tuple has a NULL (`HEAP_HASNULL`). One
  bit per attribute, LSB first, **1 = not null**. A NULL costs one bit and zero
  data bytes — nothing is written for it in the data area.
- `natts` in the tuple can be smaller than the table's column count:
  `ALTER TABLE ADD COLUMN` (without a default) does not rewrite the table; old
  tuples simply end early and the missing columns read as NULL
  (`heap_attisnull` / `getmissingattr`).

### Alignment (typalign)

`pg_type.typalign` is `c` (1), `s` (2), `i` (4) or `d` (8). Before storing an
attribute, the offset is rounded up (`att_align_nominal` → `TYPEALIGN`).
Padding bytes are zero. Because the tuple start is MAXALIGNed on the page,
aligning offsets *relative to the tuple* also aligns the real memory address,
so values can be read with a plain load.

| type | typlen | typalign |
|---|---|---|
| bool | 1 | c |
| int4 | 4 | i |
| int8, float8 | 8 | d |
| text (varlena) | -1 | i |

### Varlena: variable-length values

`text`, `bytea`, `numeric`, arrays… are *varlena*: a header holding the total
length, then the bytes (`varatt.h`). On a little-endian machine:

```
1-byte header   xxxxxxx1                 total ≤ 127 bytes, NOT aligned
4-byte header   xxxxxx00 (uint32 LE)     total = header>>2, aligned to typalign
external TOAST  00000001  (0x01)         pointer to a value stored out of line
compressed      xxxxxx10                 inline pglz/lz4-compressed datum
```

`heap_fill_tuple` converts any value that fits into the 1-byte "short" form,
saving 3 bytes *and* the alignment padding. Values larger than ~2 kB are
compressed and/or moved to the TOAST table (not covered here).

How does a reader know whether to align before a text attribute if short
headers are unaligned and long ones are aligned? **`att_align_pointer`**: if
the byte at the current (unaligned) offset is `0x00`, it must be padding
(a short header is always odd), so align; otherwise a short header starts
right here. A 4-byte header can begin with `0x00`, but it always sits on an
aligned offset where aligning is a no-op.

### Deforming and attcacheoff

`heap_deform_tuple` walks the attributes left to right with a running offset —
O(columns). To read *one* column, `nocachegetattr` uses
`TupleDesc->attrs[i].attcacheoff`: when every column before `i` is fixed width,
column `i`'s offset is the same in **every** tuple without a NULL before it,
so it is computed once and cached in the descriptor. One NULL or one varlena
earlier in the row breaks the cache for everything after it — a big reason to
put `NOT NULL` fixed-width columns first. (Postgres 18 refines this further
with `CompactAttribute`, but the idea is the same.)

### Padding and column tetris

Postgres stores columns in declaration order. `(bool, int8, bool, int8)`
wastes 14 bytes per row in alignment holes; `(int8, int8, bool, bool)` wastes
none. On a billion-row table that is gigabytes, more pages to scan, and more
WAL. DBAs reorder columns by descending `typalign` with varlenas last — a game
nicknamed "column tetris". Problem 1.15 automates it.

### Memcomparable keys (beyond Postgres)

Postgres B-trees call a type-specific comparison function for every key
comparison. Many engines (CockroachDB, MyRocks, FoundationDB's tuple layer,
TiKV) instead encode keys so that **`memcmp` order equals SQL order**: flip the
sign bit of big-endian integers, apply the sign trick to IEEE floats, escape
`0x00` in strings and terminate them, use a tag byte to place NULLs, and invert
every byte for DESC columns. The B+Tree in a later phase stores these bytes.

## Prerequisites

None — only `pkg/types`.

## Suggested order

1. 1.1 `AlignUp` — the padding rule everything else uses.
2. 1.2–1.3 `AppendFixed` / `DecodeFixed` — fixed-width types.
3. 1.4–1.5 `AppendVarlena` / `DecodeVarlena` — the two header forms.
4. 1.6–1.7 `NullBitmap` / `AttIsNull`.
5. 1.8–1.9 `AppendHeader` / `DecodeHeader`.
6. 1.10 `ComputeTupleSize` — the whole layout, without writing bytes.
7. 1.11 `FormTuple`, then 1.12 `DeformTuple` (the `att_align_pointer` trick).
8. 1.13 `GetAttr` — attcacheoff fast path.
9. 1.14 `EncodeKey` — order-preserving keys for the B+Tree phase.
10. 1.15 `ColumnTetris` — bonus: reorder columns to remove padding.
