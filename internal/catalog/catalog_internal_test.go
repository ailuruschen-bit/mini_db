package catalog

import (
	"errors"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/record"
)

// fakeStore is an in-memory blobStore whose load/save can be made to fail, so
// the catalog's error and rollback paths can be driven without touching the OS.
type fakeStore struct {
	data     []byte
	ok       bool
	failLoad error
	failSave error
}

func (s *fakeStore) load() ([]byte, bool, error) {
	if s.failLoad != nil {
		return nil, false, s.failLoad
	}
	return s.data, s.ok, nil
}

func (s *fakeStore) save(data []byte) error {
	if s.failSave != nil {
		return s.failSave
	}
	s.data, s.ok = data, true
	return nil
}

func oneCol() record.Schema {
	return record.Schema{Columns: []record.Column{{Name: "a", Type: record.TypeInt}}}
}

// A load failure surfaces from openWith.
func TestOpenWithLoadError(t *testing.T) {
	if _, err := openWith("d", &fakeStore{failLoad: errors.New("boom")}); err == nil {
		t.Error("openWith did not surface the load error")
	}
}

// A stored document with a null "tables" leaves the map nil after unmarshal;
// the catalog must recover it to an empty map and keep the loaded id counter.
func TestOpenWithMissingTablesMap(t *testing.T) {
	c, err := openWith("d", &fakeStore{data: []byte(`{"next_id":3,"tables":null}`), ok: true})
	if err != nil {
		t.Fatal(err)
	}
	tm, err := c.CreateTable("t", oneCol())
	if err != nil {
		t.Fatalf("create on a tables-less catalog: %v", err)
	}
	if tm.File != "rel_3.db" {
		t.Errorf("file = %s, want rel_3.db (id counter from the loaded state)", tm.File)
	}
}

// A save failure during CreateTable rolls back both the table and the id
// counter, so a later successful create reuses the id.
func TestCreateTableSaveRollback(t *testing.T) {
	fs := &fakeStore{}
	c, err := openWith("d", fs)
	if err != nil {
		t.Fatal(err)
	}

	fs.failSave = errors.New("boom")
	if _, err := c.CreateTable("t", oneCol()); err == nil {
		t.Error("CreateTable did not surface the save failure")
	}
	if _, ok := c.GetTable("t"); ok {
		t.Error("table remained after a failed save")
	}

	fs.failSave = nil
	tm, err := c.CreateTable("t", oneCol())
	if err != nil {
		t.Fatal(err)
	}
	if tm.File != "rel_0.db" {
		t.Errorf("file = %s, want rel_0.db (id counter rolled back)", tm.File)
	}
}

// A save failure during CreateIndex rolls back the appended index and the id
// counter, leaving the table's index list untouched.
func TestCreateIndexSaveRollback(t *testing.T) {
	fs := &fakeStore{}
	c, err := openWith("d", fs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable("t", oneCol()); err != nil { // rel_0, nextID=1
		t.Fatal(err)
	}

	fs.failSave = errors.New("boom")
	if _, err := c.CreateIndex("t", "i", 0); err == nil {
		t.Error("CreateIndex did not surface the save failure")
	}
	if tm, _ := c.GetTable("t"); len(tm.Indexes) != 0 {
		t.Errorf("index list = %+v after a failed save, want empty", tm.Indexes)
	}

	fs.failSave = nil
	im, err := c.CreateIndex("t", "i", 0)
	if err != nil {
		t.Fatal(err)
	}
	if im.File != "rel_1.db" {
		t.Errorf("file = %s, want rel_1.db (id counter rolled back)", im.File)
	}
}
