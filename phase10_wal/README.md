# Phase 10 — Write-Ahead Log & Crash Recovery

Make the database survive a power failure: encode checksummed WAL records, append them to a log buffer, read the log back and stop cleanly at a torn tail, flush it with group commit, enforce the write-ahead rule in the buffer pool, protect against torn pages with full-page images, take fuzzy checkpoints, and rebuild the exact committed state after a crash with the three ARIES passes (analysis, redo, undo with compensation log records).

## Concepts

### 1. Why a log at all

```
 UPDATE t SET … ─▶ change page in shared buffers ─▶ append WAL record ─▶ COMMIT = fsync WAL
                                    │
                                    └──── written to the data file "later" (bgwriter / checkpointer)
```

- Data pages are written **randomly** and **lazily**. Forcing every page a
  transaction touched to disk at COMMIT ("force") would make every commit cost
  many random writes. Instead Postgres forces only the **log**, which is one
  sequential append. Pages are written whenever convenient (**no-force**).
- Pages may also be written **before** their transaction commits (the buffer
  pool needs the frame — **steal**). Then the disk holds uncommitted data, and
  recovery must be able to remove it.
- *No-force* needs **redo** (re-apply committed changes missing from disk);
  *steal* needs **undo** (remove uncommitted changes that reached disk).
  ARIES gives both. Postgres needs only redo: with MVCC an aborted tuple is just
  invisible (its xmin is aborted in pg_xact), so nothing is ever physically undone.
  This phase implements the full ARIES design to show both halves.

### 2. Records and LSNs

```
 off  size  field            Postgres XLogRecord
  0    4    TotalLen         xl_tot_len
  4    4    CRC32C           xl_crc   (covers everything after it — the header fields first in PG)
  8    8    LSN              —        (PG derives it from the position; we store it to catch stale bytes)
 16    8    PrevLSN          xl_prev  (PG: previous record in the whole log; ARIES: same transaction)
 24    4    Txn              xl_xid
 28    1    Type             xl_info / xl_rmid
 29    …    body             block references, full-page images, main data
```

- An **LSN** is a byte position in the infinite WAL stream (`XLogRecPtr`,
  printed `0/16B3740`). Ordering LSNs orders events. Every page header holds
  `pd_lsn`, the LSN of the last record applied to that page.
- Postgres's WAL is cut into 16 MB **segment files** of 8 KB pages, each page
  with a small header. Old segments are **recycled** (renamed instead of
  deleted), so the bytes after the real end of the log can be a perfectly
  valid-looking *old* record. That is why the reader checks `xl_prev` (we check
  the stored LSN against the offset).
- **CRC-32C** is checked for every record (`ValidXLogRecord`). The end of the
  WAL is simply *the first record that does not validate*: a short read, zeros,
  a bad CRC, or a stale record. None of these is an error during crash recovery.

### 3. Durability: XLogFlush and group commit

```
 WAL buffers:  | flushed ...........| inserted, not yet written ......|
               0              LogwrtResult.Flush              insert position
```

- `XLogInsert` only copies the record into shared WAL buffers. `XLogFlush(lsn)`
  makes sure everything up to `lsn` is on disk: take `WALWriteLock`, **re-check**
  whether someone else already flushed far enough, otherwise write and
  `fsync` **as much as is available** — not just up to `lsn`.
- That re-check is group commit: while one backend is inside fsync, others
  queue on the lock; when they get it, most of them find their record already
  flushed and return at once. `commit_delay` / `commit_siblings` can even make
  the leader wait a moment so more commits pile up.
- `synchronous_commit = off` skips the flush at commit (the walwriter flushes
  within ~3×`wal_writer_delay`): a crash may lose the last few commits, but
  never corrupts anything.

### 4. The write-ahead rule

- A data page may reach disk only after the WAL record of its last change has:
  `FlushBuffer()` calls `XLogFlush(BufferGetLSN(buf))` before `smgrwrite`.
- Without it, a crash could leave a change on disk with no log record — not
  redoable, not undoable. Our `Disk` fake refuses such writes, so a
  wrong order fails loudly.

### 5. Torn pages and full-page writes

- An 8 KB page is written as 16 × 512 B sectors (or 2 × 4 KB). Power loss in
  the middle leaves a **torn page**: part new, part old. Its LSN (in the first
  sector) may claim it is new, so the normal "skip if page LSN ≥ record LSN"
  test would skip the very records needed to fix it.
- Postgres's answer (`full_page_writes = on`): the **first** time a page is
  modified after a checkpoint, the record carries a complete image of the page
  (a *backup block*, `XLogRecordBlockImageHeader`). Redo restores the image
  unconditionally (`BLK_RESTORED`) and replays the following records on top.
- That rule is safe in Postgres because a Postgres checkpoint writes **every**
  dirty buffer before it completes: any page written after the redo point was
  necessarily modified after it, so it has an image after the redo point.
- Our checkpoints are ARIES-style **fuzzy** (they write no pages), so the rule
  becomes "log an image when a **clean** page is first dirtied". The image is
  then exactly the page's recLSN, and redo — which starts at the smallest
  recLSN — always sees it. Same idea, one notch more conservative.
- Postgres compresses images (`wal_compression`) and skips the "hole" between
  `pd_lower` and `pd_upper`; FPIs are still often the majority of WAL volume
  right after a checkpoint, which is why checkpoints are spread out
  (`checkpoint_timeout`, `max_wal_size`).

### 6. Checkpoints

- **Postgres** (`CreateCheckPoint`): remember the current insert position as the
  **redo pointer**, write out all dirty buffers (spread over
  `checkpoint_completion_target`), write a checkpoint record, flush, update
  `pg_control`. Recovery starts redo at the redo pointer. WAL before it can be
  recycled.
- **ARIES fuzzy checkpoint** (this phase): write a record holding the Active
  Transaction Table and the Dirty Page Table, flush it, then point the **master
  record** (our `Disk.Master`, PG's `pg_control`) at it. No page is written;
  the DPT's smallest recLSN says how far back redo must go. Real ARIES uses a
  begin/end pair of records so transactions can run while the tables are copied.
- The master record is updated **after** the checkpoint record is durable —
  otherwise a crash could leave pg_control pointing at nothing.

### 7. ARIES recovery: analysis → redo → undo

```
 log:  ……  [min recLSN] ……………… [CKPT] ……………………………… [end] ~torn~
 1 Analysis                        ├────────────────────────▶   rebuild ATT + DPT
 2 Redo        ├──────────────────────────────────────────▶    repeat history
 3 Undo                         ◀──────── losers, newest LSN first, writing CLRs
```

1. **Analysis** starts at the checkpoint, seeds ATT/DPT from it and scans to the
   end: BEGIN/UPDATE/CLR put a txn in the ATT (lastLSN), COMMIT/ABORT take it
   out, every page-changing record adds its page to the DPT (keeping the first
   recLSN). What remains in the ATT are the **losers**.
2. **Redo** repeats history from the smallest recLSN — including the losers'
   changes and old CLRs — so the database is exactly as it was at the crash.
   A record is skipped if its page isn't in the DPT, if its LSN is below the
   page's recLSN, or if `pageLSN ≥ LSN` (already applied). This makes redo
   **idempotent**: a crash during recovery just means running it again.
3. **Undo** rolls back all losers together, always taking the largest LSN
   next. Each undone update writes a **CLR** ("compensation log record") that
   is redo-only and carries **UndoNext** = the PrevLSN of the record it undid.
   If the system crashes during undo, the next recovery redoes the CLRs and
   resumes at UndoNext: no change is ever undone twice, and undo work is
   bounded even under repeated crashes. When a loser is fully rolled back it
   gets an ABORT (ARIES "END") record.

Postgres's own recovery (`StartupXLOG` → `PerformWalRecovery`) is only step 2:
read the checkpoint location from `pg_control`, redo every record from the redo
pointer, using `XLogReadBufferForRedo` which returns `BLK_NEEDS_REDO`,
`BLK_DONE` (page LSN already ≥ record) or `BLK_RESTORED` (full-page image).
In-progress transactions simply never get a commit bit in pg_xact and their
tuples stay invisible until VACUUM removes them. The same redo loop runs
forever on a **streaming replica** — physical replication is "crash recovery
that never ends".

## Prerequisites

None — this phase only imports `pkg/types`. It is self-contained: a 64-byte
`Page` with an LSN, an in-memory `Disk` that is the only thing to survive
`DB.Crash`, a `Log` (buffer in front of the disk's WAL) and a no-eviction
`BufferPool`. The `Disk` enforces the WAL rule and can tear the next page write;
`Log.fsync` counts fsyncs and detects concurrent ones; `SyncHook` lets tests
block the "device" to observe group commit.

## Suggested order

1. **10.1–10.2** the record format: encode with CRC, decode and validate.
2. **10.3–10.4** append to the buffer, read the log back and find its end.
3. **10.5** `FlushUpTo` with group commit — run it with `-race`.
4. **10.6** the WAL rule in `FlushPage`.
5. **10.7–10.8** logging an update (with full-page images) and committing.
6. **10.9** redo of a single record (page LSN test, FPI restore).
7. **10.10** fuzzy checkpoint, **10.11** analysis, **10.12** redo pass.
8. **10.13** undo with CLRs, then **10.14** `Recover`, whose seeded
   random-crash test (torn pages, torn WAL tails, crashes right after recovery)
   is the final exam.

Run one problem's tests with `go test -run '^TestP10_5_' ./phase10_wal/`.
