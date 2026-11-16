package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP11_1_WriteFileAtomic_NewAndReplace(t *testing.T) {
	for _, sync := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "catalog.json")
		if err := WriteFileAtomic(path, []byte(`{"NextRelID":16385}`), sync); err != nil {
			t.Fatalf("sync=%v: first write: %v", sync, err)
		}
		if got, _ := os.ReadFile(path); string(got) != `{"NextRelID":16385}` {
			t.Fatalf("sync=%v: content = %q", sync, got)
		}
		if err := WriteFileAtomic(path, []byte("v2"), sync); err != nil {
			t.Fatalf("sync=%v: replace: %v", sync, err)
		}
		if got, _ := os.ReadFile(path); string(got) != "v2" {
			t.Fatalf("sync=%v: content after replace = %q, want %q (old bytes must not survive)", sync, got, "v2")
		}
		if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("sync=%v: %s.tmp still exists (err %v)", sync, path, err)
		}
	}
}

func TestP11_1_WriteFileAtomic_FailureKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make the temporary file impossible to create: a directory is in the way.
	if err := os.MkdirAll(filepath.Join(path+".tmp", "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), false); err == nil {
		t.Fatal("WriteFileAtomic succeeded although the tmp file cannot be created")
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("content = %q, want the old file untouched", got)
	}
	if _, err := os.Stat(filepath.Join(path+".tmp", "blocker")); err != nil {
		t.Fatalf("the directory in the way was removed: %v", err)
	}
}

func TestP11_1_WriteFileAtomic_MissingDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope", "catalog.json")
	if err := WriteFileAtomic(path, []byte("x"), false); err == nil {
		t.Fatal("want an error for a missing directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was created: %v", err)
	}
}

// CreateTable persists the catalog through WriteFileAtomic; a reopen sees it.
func TestP11_1_CatalogSurvivesReopen(t *testing.T) {
	db := newDB(t, Options{})
	mustCreate(t, db, "emp", empSchema())
	mustCreate(t, db, "dept", deptSchema())
	if err := db.CreateTable("emp", deptSchema()); !errors.Is(err, ErrTableExists) {
		t.Fatalf("duplicate CreateTable: err = %v, want ErrTableExists", err)
	}
	if err := db.CreateTable("bad", types.NewSchema("a", types.KindInt32, "a", types.KindText)); !errors.Is(err, ErrBadSchema) {
		t.Fatalf("duplicate column: err = %v, want ErrBadSchema", err)
	}

	b, err := os.ReadFile(filepath.Join(db.Dir(), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Catalog
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatalf("catalog.json is not JSON: %v", err)
	}
	if onDisk.NextRelID != 16386 || len(onDisk.Tables) != 2 {
		t.Fatalf("catalog.json: NextRelID %d, %d tables; want 16386, 2", onDisk.NextRelID, len(onDisk.Tables))
	}

	db2 := reopen(t, db, Options{})
	cat := db2.Catalog()
	if len(cat.Tables) != 2 || cat.Tables[0].Name != "emp" || cat.Tables[0].RelID != 16384 ||
		cat.Tables[1].Name != "dept" || cat.Tables[1].RelID != 16385 {
		t.Fatalf("reopened catalog tables = %+v %+v", cat.Tables[0], cat.Tables[1])
	}
	if got := cat.Table("emp").Schema.Columns[3]; got.Name != "salary" || got.Kind != types.KindInt64 {
		t.Fatalf("emp column 3 = %+v, want salary int8", got)
	}
	if _, err := os.Stat(filepath.Join(db2.Dir(), "base", "16385")); err != nil {
		t.Fatalf("heap file of dept missing: %v", err)
	}
	mustCreate(t, db2, "t3", deptSchema())
	if db2.Catalog().Table("t3").RelID != 16386 {
		t.Fatalf("next table RelID = %d, want 16386", db2.Catalog().Table("t3").RelID)
	}
}
