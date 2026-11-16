package engine

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/natthphong/daTarbase/pkg/types"
)

// FirstRelID is the first relation id handed out to a user table
// (Postgres: FirstNormalObjectId = 16384; lower OIDs belong to the system).
const FirstRelID = 16384

// TableDef is one table of the catalog (Postgres: a pg_class row plus its
// pg_attribute rows).
type TableDef struct {
	Name   string       // table name, unique
	RelID  uint32       // file name of the heap under base/ (Postgres: relfilenode)
	Schema types.Schema // columns in declaration order
}

// IndexDef is one B+Tree index of the catalog (Postgres: pg_index).
type IndexDef struct {
	Name    string   // index name, unique across the database
	Table   string   // indexed table
	Columns []string // key columns, in key order (the first one is the "leading" column)
	Include []string // INCLUDE columns: stored in the leaf entries, not part of the key
}

// Catalog is every table and index definition. It is saved as JSON in
// <dir>/catalog.json after every DDL statement and loaded by Open.
type Catalog struct {
	NextRelID uint32      // RelID of the next table to be created
	Tables    []*TableDef // in creation order
	Indexes   []*IndexDef // in creation order
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog { return &Catalog{NextRelID: FirstRelID} }

// Table returns the definition of the named table, or nil.
func (c *Catalog) Table(name string) *TableDef {
	for _, t := range c.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Index returns the definition of the named index, or nil.
func (c *Catalog) Index(name string) *IndexDef {
	for _, ix := range c.Indexes {
		if ix.Name == name {
			return ix
		}
	}
	return nil
}

// IndexesOf returns the indexes of a table, in creation order.
func (c *Catalog) IndexesOf(table string) []*IndexDef {
	var out []*IndexDef
	for _, ix := range c.Indexes {
		if ix.Table == table {
			out = append(out, ix)
		}
	}
	return out
}

// Save writes the catalog to path as JSON with WriteFileAtomic (Problem 11.1).
func (c *Catalog) Save(path string, sync bool) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(b, '\n'), sync)
}

// LoadCatalog reads a catalog written by Save. A missing file means a brand-new
// database: an empty catalog. A leftover path+".tmp" from a crash is ignored.
func LoadCatalog(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewCatalog(), nil
	}
	if err != nil {
		return nil, err
	}
	c := NewCatalog()
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Problem 11.1 — WriteFileAtomic [Easy]
//
// Replace the file at path with data so that a crash leaves either the complete old file or the complete new one.
//
// os.WriteFile truncates the file first: a crash in the middle leaves a half
// written catalog and the database cannot start. Postgres writes control files
// and catalogs' relation maps the "durable rename" way (durable_rename() in
// src/backend/storage/file/fd.c): write a temporary file, fsync it, rename it
// over the old one (rename is atomic on POSIX file systems), then fsync the
// directory so the rename itself survives a power loss.
//
//	catalog.json.tmp  ← write data, fsync, close
//	rename(catalog.json.tmp, catalog.json)
//	fsync(dir of catalog.json)
//
// Rules:
//  1. Create (or truncate) tmp = path + ".tmp", write all of data to it.
//  2. If sync is true, fsync tmp (File.Sync) before closing it. If sync is
//     false, never call Sync (tests run with sync off for speed).
//  3. Close tmp, then os.Rename(tmp, path).
//  4. If sync is true, open the directory of path (filepath.Dir) and Sync it.
//  5. If creating tmp fails, return that error (nothing was created). On any
//     later error up to and including the rename: remove tmp (ignore that
//     error) and return the first error. Either way path is exactly as it was.
//
// Example:
//
//	WriteFileAtomic("db/catalog.json", []byte(`{"NextRelID":16385}`), false)
//	// db/catalog.json holds exactly those bytes; db/catalog.json.tmp does not exist
//	// path in a directory that does not exist → error, nothing created
//
// Constraints: one write of data; never truncate path itself.
// Postgres reference: durable_rename() in src/backend/storage/file/fd.c
func WriteFileAtomic(path string, data []byte, sync bool) error {
	panic("TODO: Problem 11.1")
}
