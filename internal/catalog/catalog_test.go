package catalog_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/catalog"
	"github.com/ailuruschen-bit/minidb/internal/record"
)

func sampleSchema() record.Schema {
	return record.Schema{Columns: []record.Column{
		{Name: "id", Type: record.TypeInt},
		{Name: "name", Type: record.TypeText, Nullable: true},
	}}
}

// open returns a catalog under a fresh temp dir.
func open(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	return c
}

// A created table reads back by name with its schema and assigned file; a
// missing table is reported absent.
func TestCreateAndGetTable(t *testing.T) {
	c := open(t)

	tm, err := c.CreateTable("users", sampleSchema())
	if err != nil {
		t.Fatal(err)
	}
	if tm.Name != "users" || tm.File != "rel_0.db" {
		t.Errorf("meta = %+v, want name=users file=rel_0.db", tm)
	}
	if !reflect.DeepEqual(tm.Schema, sampleSchema()) {
		t.Errorf("schema = %+v, want %+v", tm.Schema, sampleSchema())
	}

	got, ok := c.GetTable("users")
	if !ok {
		t.Fatal("GetTable(users) not found after create")
	}
	if !reflect.DeepEqual(got, tm) {
		t.Errorf("GetTable = %+v, want %+v", got, tm)
	}

	if _, ok := c.GetTable("missing"); ok {
		t.Error("GetTable(missing) reported present")
	}
}

// File names come from one shared counter across tables and indexes.
func TestFileNamesAssignedByCounter(t *testing.T) {
	c := open(t)

	t0, _ := c.CreateTable("a", sampleSchema())
	t1, _ := c.CreateTable("b", sampleSchema())
	ix, err := c.CreateIndex("a", "a_id", 0)
	if err != nil {
		t.Fatal(err)
	}
	if t0.File != "rel_0.db" || t1.File != "rel_1.db" || ix.File != "rel_2.db" {
		t.Errorf("files = %s, %s, %s; want rel_0/1/2.db", t0.File, t1.File, ix.File)
	}
}

func TestCreateTableErrors(t *testing.T) {
	c := open(t)
	if _, err := c.CreateTable("", sampleSchema()); !errors.Is(err, catalog.ErrEmptyName) {
		t.Errorf("empty name err = %v, want ErrEmptyName", err)
	}
	if _, err := c.CreateTable("t", record.Schema{}); !errors.Is(err, catalog.ErrNoColumns) {
		t.Errorf("no columns err = %v, want ErrNoColumns", err)
	}
	if _, err := c.CreateTable("t", sampleSchema()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable("t", sampleSchema()); !errors.Is(err, catalog.ErrTableExists) {
		t.Errorf("duplicate err = %v, want ErrTableExists", err)
	}
}

// An index records its column and file, and appears on its table's metadata.
func TestCreateIndex(t *testing.T) {
	c := open(t)
	if _, err := c.CreateTable("users", sampleSchema()); err != nil {
		t.Fatal(err)
	}

	im, err := c.CreateIndex("users", "users_id", 0)
	if err != nil {
		t.Fatal(err)
	}
	if im.Name != "users_id" || im.Column != 0 || im.File != "rel_1.db" {
		t.Errorf("index meta = %+v", im)
	}

	tm, _ := c.GetTable("users")
	if len(tm.Indexes) != 1 || tm.Indexes[0] != im {
		t.Errorf("table indexes = %+v, want [%+v]", tm.Indexes, im)
	}
}

func TestCreateIndexErrors(t *testing.T) {
	c := open(t)
	if _, err := c.CreateTable("users", sampleSchema()); err != nil { // 2 columns
		t.Fatal(err)
	}

	if _, err := c.CreateIndex("users", "", 0); !errors.Is(err, catalog.ErrEmptyName) {
		t.Errorf("empty name err = %v, want ErrEmptyName", err)
	}
	if _, err := c.CreateIndex("missing", "i", 0); !errors.Is(err, catalog.ErrTableNotFound) {
		t.Errorf("missing table err = %v, want ErrTableNotFound", err)
	}
	for _, col := range []int{-1, 2, 99} {
		if _, err := c.CreateIndex("users", "i", col); !errors.Is(err, catalog.ErrBadColumn) {
			t.Errorf("column %d err = %v, want ErrBadColumn", col, err)
		}
	}
	if _, err := c.CreateIndex("users", "dup", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex("users", "dup", 1); !errors.Is(err, catalog.ErrIndexExists) {
		t.Errorf("duplicate index err = %v, want ErrIndexExists", err)
	}
}

// ListTables returns every table, ordered by name, as independent copies.
func TestListTablesSorted(t *testing.T) {
	c := open(t)
	for _, name := range []string{"charlie", "alice", "bob"} {
		if _, err := c.CreateTable(name, sampleSchema()); err != nil {
			t.Fatal(err)
		}
	}
	got := c.ListTables()
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	if !reflect.DeepEqual(names, []string{"alice", "bob", "charlie"}) {
		t.Errorf("ListTables order = %v, want sorted", names)
	}
}

// Metadata handed to callers is a copy: mutating it does not corrupt the catalog.
func TestReturnedCopiesAreIndependent(t *testing.T) {
	c := open(t)
	if _, err := c.CreateTable("t", sampleSchema()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex("t", "i", 0); err != nil {
		t.Fatal(err)
	}

	tm, _ := c.GetTable("t")
	tm.Schema.Columns[0].Name = "hacked"
	tm.Indexes[0].Name = "hacked"

	fresh, _ := c.GetTable("t")
	if fresh.Schema.Columns[0].Name != "id" {
		t.Error("mutating a returned schema reached into the catalog")
	}
	if fresh.Indexes[0].Name != "i" {
		t.Error("mutating a returned index list reached into the catalog")
	}
}

// The catalog survives a close/reopen: tables, indexes and the id counter all
// persist, so a table created after reopen gets the next file id.
func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	c1, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c1.CreateTable("users", sampleSchema()); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.CreateIndex("users", "users_id", 0); err != nil {
		t.Fatal(err)
	}

	c2, err := catalog.Open(dir) // reopen the same directory
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	tm, ok := c2.GetTable("users")
	if !ok {
		t.Fatal("reopened catalog lost the users table")
	}
	if len(tm.Indexes) != 1 || tm.Indexes[0].Name != "users_id" {
		t.Errorf("reopened indexes = %+v", tm.Indexes)
	}
	// nextID persisted (2 ids used), so the next table's file is rel_2.db.
	next, err := c2.CreateTable("orders", sampleSchema())
	if err != nil {
		t.Fatal(err)
	}
	if next.File != "rel_2.db" {
		t.Errorf("post-reopen file = %s, want rel_2.db (counter persisted)", next.File)
	}
}

// Dir reports the directory the catalog was opened under, so callers can
// resolve a meta's relative File to a full path.
func TestDir(t *testing.T) {
	dir := t.TempDir()
	c, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", c.Dir(), dir)
	}
}

// Open reports an error when the directory cannot be created (a path component
// is a file).
func TestOpenMkdirFailure(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Open(filepath.Join(f, "sub")); err == nil {
		t.Error("Open under a file path did not error")
	}
}

// A catalog file that cannot be read (here, it is a directory) surfaces a load
// error from Open.
func TestOpenLoadFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "catalog.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Open(dir); err == nil {
		t.Error("Open with an unreadable catalog file did not error")
	}
}

// A corrupt catalog file surfaces a parse error.
func TestOpenParseFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Open(dir); err == nil {
		t.Error("Open accepted a corrupt catalog file")
	}
}

// A failed catalog write surfaces from CreateTable and rolls the change back.
func TestSaveWriteFailure(t *testing.T) {
	dir := t.TempDir()
	c, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Block the temp-file write by occupying its path with a directory.
	if err := os.Mkdir(filepath.Join(dir, "catalog.json.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable("t", sampleSchema()); err == nil {
		t.Error("CreateTable did not surface the write failure")
	}
	if _, ok := c.GetTable("t"); ok {
		t.Error("a failed CreateTable left the table in the catalog")
	}
}
