package wal

import "github.com/natthphong/daTarbase/pkg/types"

// Problem 10.4 — ReadRecords [Medium]
//
// Decode every valid record from LSN `from` onward and return them plus the end of the valid log, stopping cleanly at a torn tail.
//
// After a crash the WAL ends in one of these ways:
//
//	... | rec | rec |                      clean end (no more bytes)
//	... | rec | rec | rec-prefix           write cut short   → ErrShortRecord
//	... | rec | rec | 00 00 00 00 ...      pre-zeroed space  → ErrCorruptRecord
//	... | rec | rec | rec with bad bytes   torn sector       → ErrBadCRC
//	... | rec | rec | old valid record     leftover from a recycled file (wrong LSN)
//
// None of these is an error for recovery: they simply mark where the valid log
// ends. Recovery replays up to there and appends new records from there.
//
// Rules:
//  1. from < FirstLSN or from > len(log) → nil, 0, ErrInvalidLSN.
//  2. Starting at offset from, repeatedly DecodeRecord (Problem 10.2) the bytes
//     log[pos:]. Stop (without error) when pos == len(log) or decoding fails.
//  3. Also stop if a record decodes fine but its stored LSN != pos: it is a
//     stale record, not part of this log.
//  4. Return the decoded records in log order and end = the offset just past the
//     last valid record (== from if none). The error is nil whenever rule 1 passes.
//
// Example (log = header + BEGIN@8 (29 B) + UPDATE@37 (34 B) + COMMIT@71 (29 B), len 100):
//
//	ReadRecords(log, 8)            // → [BEGIN@8, UPDATE@37, COMMIT@71], end 100
//	ReadRecords(log, 37)           // → [UPDATE@37, COMMIT@71], end 100
//	ReadRecords(log, 100)          // → [], end 100
//	ReadRecords(log[:90], 8)       // → [BEGIN@8, UPDATE@37], end 71   (torn COMMIT)
//	ReadRecords(log, 7)            // → ErrInvalidLSN
//
// Constraints: O(len(log) - from).
// Postgres reference: XLogReadRecord() / XLogDecodeNextRecord() in src/backend/access/transam/xlogreader.c
func ReadRecords(log []byte, from types.LSN) ([]Record, types.LSN, error) {
	if from < FirstLSN || int(from) > len(log) {
		return nil, 0, ErrInvalidLSN
	}
	var out []Record
	pos := from
	for int(pos) < len(log) {
		r, n, err := DecodeRecord(log[pos:])
		if err != nil || r.LSN != pos {
			break
		}
		out = append(out, r)
		pos += types.LSN(n)
	}
	return out, pos, nil
}
