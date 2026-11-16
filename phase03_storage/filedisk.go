package storage

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/natthphong/daTarbase/pkg/types"
)

// File is the part of *os.File that FileDisk uses. Tests substitute fakes
// that misbehave (short writes, I/O errors).
//
// The io.ReaderAt contract applies: when ReadAt returns n < len(p) it also
// returns a non-nil error (io.EOF at end of file).
type File interface {
	io.ReaderAt
	io.WriterAt
	Close() error
}

// FileDisk stores a relation as ONE file: page i lives at byte offset
// i*PageSize. It is not safe for concurrent use.
//
//	file:  | page 0 | page 1 | page 2 | ...
//	offset 0        8192     16384
type FileDisk struct {
	f        File   // the underlying file
	numPages uint32 // number of allocated pages (file size / PageSize)
}

var (
	_ DiskManager = (*FileDisk)(nil)
	_ DiskManager = (*MemDisk)(nil)
)

// OpenFileDisk opens (creating if needed) the file at path. An existing file
// of size S holds S/PageSize pages (a trailing partial page is ignored).
func OpenFileDisk(path string) (*FileDisk, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &FileDisk{f: f, numPages: uint32(st.Size() / PageSize)}, nil
}

// NewFileDisk wraps an already-open File that holds numPages pages.
func NewFileDisk(f File, numPages uint32) *FileDisk { return &FileDisk{f: f, numPages: numPages} }

// NumPages is the number of allocated pages.
func (d *FileDisk) NumPages() uint32 { return d.numPages }

// Close closes the underlying file.
func (d *FileDisk) Close() error { return d.f.Close() }

// Problem 3.1 — FileDisk.ReadPage [Easy]
//
// Read page id of a single-file relation into buf.
//
// Page id lives at byte offset id*PageSize (8192). Compute the offset in
// int64: id*8192 overflows 32 bits for id >= 524288 (a 4 GB file).
//
//	id=0 -> offset 0, id=2 -> offset 16384, id=600000 -> offset 4915200000
//
// Rules:
//  1. len(buf) != PageSize -> ErrBadBufferSize (no I/O).
//  2. id >= NumPages() -> ErrPageNotFound (no I/O): reading past the end of
//     the relation is an error, not a page of zeros.
//  3. Call d.f.ReadAt(buf, offset) exactly once.
//     - n == PageSize -> success (return nil even if ReadAt also returned
//     io.EOF, which is allowed when the read ends exactly at end of file).
//     - n < PageSize and the error is io.EOF -> the file is shorter than
//     NumPages() says (someone truncated it): return an error for which
//     errors.Is(err, ErrPageNotFound) is true.
//     - any other error -> return an error for which errors.Is(err, thatErr)
//     is true.
//
// Example:
//
//	// file of 3 pages, page 2 starts with "page-2"
//	d.ReadPage(2, buf)  // nil, buf[:6] == "page-2", ReadAt offset 16384
//	d.ReadPage(3, buf)  // ErrPageNotFound
//
// Constraints: no allocation besides error values.
// Postgres reference: mdread() in src/backend/storage/smgr/md.c
func (d *FileDisk) ReadPage(id types.PageID, buf []byte) error {
	if len(buf) != PageSize {
		return ErrBadBufferSize
	}
	if uint32(id) >= d.numPages {
		return ErrPageNotFound
	}
	n, err := d.f.ReadAt(buf, int64(id)*PageSize)
	if n == PageSize {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("read page %d: got %d of %d bytes: %w", id, n, PageSize, ErrPageNotFound)
	}
	return fmt.Errorf("read page %d: %w", id, err)
}

// Problem 3.2 — FileDisk.WritePage [Easy]
//
// Write buf over the existing page id, retrying short writes until all 8192 bytes are on disk.
//
// A WriteAt call may legally write FEWER bytes than asked and still return
// err == nil (a "short write", e.g. a disk filling up, a signal, a network FS).
// Real storage code must loop until the whole page is written.
//
// Rules:
//  1. len(buf) != PageSize -> ErrBadBufferSize (no I/O).
//  2. id >= NumPages() -> ErrPageNotFound (no I/O). WritePage only
//     overwrites existing pages; new pages come from AllocatePage.
//  3. Write buf at offset int64(id)*PageSize. If WriteAt wrote n < remaining
//     bytes with err == nil, call WriteAt again for the rest of buf at
//     the advanced offset, until all PageSize bytes are written.
//  4. If a WriteAt call returns err != nil, stop and return an error for
//     which errors.Is(err, thatErr) is true.
//  5. If a WriteAt call returns n == 0 and err == nil, stop and return
//     ErrShortWrite (otherwise you would loop forever).
//
// Example:
//
//	// a file that accepts at most 3000 bytes per WriteAt call
//	d.WritePage(1, buf)  // WriteAt(off=8192,  8192 bytes) -> n=3000
//	                     // WriteAt(off=11192, 5192 bytes) -> n=3000
//	                     // WriteAt(off=14192, 2192 bytes) -> n=2192; return nil
//
// Constraints: no allocation besides error values.
// Postgres reference: mdwrite() in src/backend/storage/smgr/md.c
func (d *FileDisk) WritePage(id types.PageID, buf []byte) error {
	if len(buf) != PageSize {
		return ErrBadBufferSize
	}
	if uint32(id) >= d.numPages {
		return ErrPageNotFound
	}
	off := int64(id) * PageSize
	for done := 0; done < PageSize; {
		n, err := d.f.WriteAt(buf[done:], off+int64(done))
		done += n
		if err != nil {
			return fmt.Errorf("write page %d: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("write page %d: wrote %d of %d bytes: %w", id, done, PageSize, ErrShortWrite)
		}
	}
	return nil
}

// Problem 3.3 — FileDisk.AllocatePage [Easy]
//
// Extend the file by one zero-filled page and return the new page's id.
//
// Rules:
//  1. The new page id is the current NumPages(); it is written at offset
//     id*PageSize with PageSize zero bytes (same short-write rules as 3.2).
//  2. On success NumPages() grows by 1.
//  3. If writing fails, return the error and leave NumPages() unchanged.
//  4. Page id InvalidPageID (0xFFFFFFFF) is reserved: if NumPages() is
//     already uint32(types.InvalidPageID), return ErrTooManyPages (no I/O).
//
// Example:
//
//	d, _ := OpenFileDisk("rel")  // empty file
//	d.AllocatePage()             // 0, file is 8192 bytes of zeros
//	d.AllocatePage()             // 1, file is 16384 bytes
//
// Hint: you may reuse WritePage.
// Postgres reference: mdextend() in src/backend/storage/smgr/md.c
func (d *FileDisk) AllocatePage() (types.PageID, error) {
	if d.numPages == uint32(types.InvalidPageID) {
		return 0, ErrTooManyPages
	}
	id := types.PageID(d.numPages)
	d.numPages++
	if err := d.WritePage(id, make([]byte, PageSize)); err != nil {
		d.numPages--
		return 0, err
	}
	return id, nil
}
