package storage

import (
	"fmt"
	"os"

	"github.com/natthphong/daTarbase/pkg/types"
)

// PostgresSegPages is Postgres's RELSEG_SIZE: 131072 pages * 8 KB = 1 GB per
// segment file. Tests use tiny values (e.g. 4) so files stay small.
const PostgresSegPages = 131072

// SegmentPath is the file name of segment segNo of relation base, Postgres
// style: segment 0 is "base" itself, segment n>0 is "base.n".
//
//	SegmentPath("16384", 0) == "16384"
//	SegmentPath("16384", 2) == "16384.2"
func SegmentPath(base string, segNo uint32) string {
	if segNo == 0 {
		return base
	}
	return fmt.Sprintf("%s.%d", base, segNo)
}

// SegmentedDisk spreads one relation over several files, each holding at
// most segPages pages:
//
//	segPages = 4
//	rel   : pages 0  1  2  3      (local page 0..3)
//	rel.1 : pages 4  5  6  7      (local page 0..3)
//	rel.2 : pages 8  9            (last segment may be partial)
//
// Invariant: every segment except the last holds exactly segPages pages.
// It is not safe for concurrent use.
type SegmentedDisk struct {
	base     string      // path of segment 0
	segPages uint32      // pages per segment file (>= 1)
	segs     []*FileDisk // open segments, segs[i] is SegmentPath(base, i); never empty
}

var _ DiskManager = (*SegmentedDisk)(nil)

// OpenSegmentedDisk opens segment 0 (creating it if needed) and then every
// existing "base.1", "base.2", ... until one is missing.
func OpenSegmentedDisk(base string, segPages uint32) (*SegmentedDisk, error) {
	if segPages == 0 {
		return nil, fmt.Errorf("storage: segPages must be >= 1")
	}
	d := &SegmentedDisk{base: base, segPages: segPages}
	for i := uint32(0); ; i++ {
		p := SegmentPath(base, i)
		if i > 0 {
			if _, err := os.Stat(p); err != nil {
				break
			}
		}
		fd, err := OpenFileDisk(p)
		if err != nil {
			d.Close()
			return nil, err
		}
		d.segs = append(d.segs, fd)
	}
	return d, nil
}

// NumPages is the total number of pages over all segments.
func (d *SegmentedDisk) NumPages() uint32 {
	last := len(d.segs) - 1
	return uint32(last)*d.segPages + d.segs[last].NumPages()
}

// NumSegments is the number of segment files currently open.
func (d *SegmentedDisk) NumSegments() int { return len(d.segs) }

// ReadPage reads global page id.
func (d *SegmentedDisk) ReadPage(id types.PageID, buf []byte) error {
	seg, local, err := d.Locate(id)
	if err != nil {
		return err
	}
	return seg.ReadPage(local, buf)
}

// WritePage writes global page id.
func (d *SegmentedDisk) WritePage(id types.PageID, buf []byte) error {
	seg, local, err := d.Locate(id)
	if err != nil {
		return err
	}
	return seg.WritePage(local, buf)
}

// Close closes every segment file.
func (d *SegmentedDisk) Close() error {
	var first error
	for _, s := range d.segs {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Problem 3.4 — SegmentFor [Easy]
//
// Map a relation-wide page number to (segment number, page number inside that segment).
//
// Postgres never lets one file grow past 1 GB: relation 16384 is stored in
// files 16384, 16384.1, 16384.2, ... each holding segPages pages (131072 by
// default). Page id lives in segment id/segPages at local page id%segPages.
//
// Rules:
//  1. segNo = id / segPages, off = id % segPages.
//  2. segPages is always >= 1 (callers guarantee it).
//
// Example:
//
//	SegmentFor(9, 4)                       // (2, 1)
//	SegmentFor(131072, PostgresSegPages)   // (1, 0)  first page of "16384.1"
//
// Constraints: O(1).
// Postgres reference: _mdfd_getseg() / the RELSEG_SIZE arithmetic in src/backend/storage/smgr/md.c
func SegmentFor(id types.PageID, segPages uint32) (segNo uint32, off types.PageID) {
	panic("TODO: Problem 3.4")
}

// Problem 3.5 — SegmentedDisk.Locate [Easy]
//
// Find the open segment file and local page number that hold global page id.
//
// Used by the provided ReadPage/WritePage, which then call ReadPage/WritePage
// on the returned *FileDisk with the returned local page number.
//
// Rules:
//  1. Use SegmentFor(id, d.segPages).
//  2. If that segment is not open (segNo >= number of segments), or the
//     local page is not allocated in it (local >= segment.NumPages()),
//     return ErrPageNotFound.
//
// Example (segPages=4, 10 pages allocated: rel=4, rel.1=4, rel.2=2 pages):
//
//	d.Locate(5)   // (rel.1's FileDisk, 1, nil)
//	d.Locate(9)   // (rel.2's FileDisk, 1, nil)
//	d.Locate(10)  // ErrPageNotFound (rel.2 holds only 2 pages)
//	d.Locate(13)  // ErrPageNotFound (rel.3 does not exist)
//
// Constraints: O(1), no I/O.
// Postgres reference: _mdfd_getseg() in src/backend/storage/smgr/md.c
func (d *SegmentedDisk) Locate(id types.PageID) (*FileDisk, types.PageID, error) {
	panic("TODO: Problem 3.5")
}

// Problem 3.6 — SegmentedDisk.AllocatePage [Medium]
//
// Append a zeroed page to the relation, creating the next segment file when the last one is full.
//
// Rules:
//  1. Let last be the last open segment. If last.NumPages() < segPages,
//     allocate the page in last.
//  2. Otherwise (last is full) open/create the next segment file
//     SegmentPath(base, len(segs)) with OpenFileDisk, append it to segs and
//     allocate the page there. Segment files are created lazily: "rel.2"
//     must not exist until page 2*segPages is allocated.
//  3. Return the GLOBAL page id: segNo*segPages + local id.
//  4. If opening the new segment or allocating fails, return the error and
//     leave the disk unchanged (same segments, same NumPages()).
//
// Example (segPages=4, empty relation "rel"):
//
//	9 calls return 0,1,...,8
//	files: rel = 32768 bytes, rel.1 = 32768 bytes, rel.2 = 8192 bytes, no rel.3
//
// Constraints: O(1) besides the file I/O.
// Postgres reference: mdextend() + _mdfd_openseg() in src/backend/storage/smgr/md.c
func (d *SegmentedDisk) AllocatePage() (types.PageID, error) {
	panic("TODO: Problem 3.6")
}
