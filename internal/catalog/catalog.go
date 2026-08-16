// Package catalog is the system catalog: the store of metadata about the data —
// which tables exist, their schemas, the file backing each, and their indexes.
// It is the name-resolution authority the Table and SQL layers build on.
//
// The catalog persists its metadata as a JSON document behind a small store
// interface, so the storage can later be replaced (e.g. by self-describing heap
// tables) without changing this package's interface or the layers above it. The
// catalog records file *names* only; opening files and wiring the buffer pool,
// heap and indexes together is the Table layer's job.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ailuruschen-bit/minidb/internal/record"
)

// Errors reported by the catalog. Callers can match them with errors.Is.
var (
	ErrTableExists   = errors.New("catalog: table already exists")
	ErrTableNotFound = errors.New("catalog: table not found")
	ErrIndexExists   = errors.New("catalog: index already exists")
	ErrBadColumn     = errors.New("catalog: column index out of range")
	ErrEmptyName     = errors.New("catalog: empty name")
	ErrNoColumns     = errors.New("catalog: table needs at least one column")
)

// IndexMeta describes one index: its name, the column it is built on (an index
// into the table's schema), and the file backing it.
type IndexMeta struct {
	Name   string `json:"name"`
	Column int    `json:"column"`
	File   string `json:"file"`
}

// TableMeta describes one table: its name, column schema, the heap file backing
// it, and its indexes.
type TableMeta struct {
	Name    string        `json:"name"`
	Schema  record.Schema `json:"schema"`
	File    string        `json:"file"`
	Indexes []IndexMeta   `json:"indexes"`
}

// clone deep-copies the slices a caller could otherwise mutate to corrupt the
// catalog's in-memory state.
func (t TableMeta) clone() TableMeta {
	t.Schema.Columns = append([]record.Column(nil), t.Schema.Columns...)
	t.Indexes = append([]IndexMeta(nil), t.Indexes...)
	return t
}

// state is the persisted document: a monotonic id counter (for file names) and
// the tables by name.
type state struct {
	NextID uint32               `json:"next_id"`
	Tables map[string]TableMeta `json:"tables"`
}

// Catalog holds the metadata in memory and persists it through a store on every
// change. Its methods are safe for concurrent use.
type Catalog struct {
	mu    sync.Mutex
	dir   string
	store blobStore
	state state
}

// Open loads the catalog under dir, creating dir and an empty catalog if none
// exists yet.
func Open(dir string) (*Catalog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("catalog: mkdir %s: %w", dir, err)
	}
	return openWith(dir, fileStore{path: filepath.Join(dir, "catalog.json")})
}

// openWith is the store-injectable core of Open; tests supply a fake store.
func openWith(dir string, store blobStore) (*Catalog, error) {
	c := &Catalog{dir: dir, store: store, state: state{Tables: map[string]TableMeta{}}}
	data, ok, err := store.load()
	if err != nil {
		return nil, fmt.Errorf("catalog: load: %w", err)
	}
	if ok {
		if err := json.Unmarshal(data, &c.state); err != nil {
			return nil, fmt.Errorf("catalog: parse: %w", err)
		}
		if c.state.Tables == nil {
			c.state.Tables = map[string]TableMeta{}
		}
	}
	return c, nil
}

// Dir returns the directory the catalog and its files live in, so a caller can
// resolve a meta's File to a full path with filepath.Join(cat.Dir(), meta.File).
func (c *Catalog) Dir() string { return c.dir }

// CreateTable records a new table with the given schema, assigns it a heap file,
// persists, and returns its metadata. It fails if the name is empty, the schema
// has no columns, or a table of that name already exists.
func (c *Catalog) CreateTable(name string, schema record.Schema) (TableMeta, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if name == "" {
		return TableMeta{}, ErrEmptyName
	}
	if len(schema.Columns) == 0 {
		return TableMeta{}, ErrNoColumns
	}
	if _, ok := c.state.Tables[name]; ok {
		return TableMeta{}, fmt.Errorf("catalog: %q: %w", name, ErrTableExists)
	}

	id := c.state.NextID
	// Own a copy of the caller's columns so a later mutation of their schema
	// cannot reach into the catalog.
	tm := TableMeta{
		Name:   name,
		Schema: record.Schema{Columns: append([]record.Column(nil), schema.Columns...)},
		File:   fileName(id),
	}

	c.state.Tables[name] = tm
	c.state.NextID = id + 1
	if err := c.persist(); err != nil {
		delete(c.state.Tables, name) // roll back on a failed write
		c.state.NextID = id
		return TableMeta{}, err
	}
	return tm.clone(), nil
}

// CreateIndex records a new index on a table's column, assigns it a file,
// persists, and returns its metadata. It fails if the table does not exist, the
// column is out of range, or the table already has an index of that name.
func (c *Catalog) CreateIndex(table, name string, column int) (IndexMeta, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if name == "" {
		return IndexMeta{}, ErrEmptyName
	}
	tm, ok := c.state.Tables[table]
	if !ok {
		return IndexMeta{}, fmt.Errorf("catalog: %q: %w", table, ErrTableNotFound)
	}
	if column < 0 || column >= len(tm.Schema.Columns) {
		return IndexMeta{}, fmt.Errorf("catalog: column %d: %w", column, ErrBadColumn)
	}
	for _, ix := range tm.Indexes {
		if ix.Name == name {
			return IndexMeta{}, fmt.Errorf("catalog: index %q: %w", name, ErrIndexExists)
		}
	}

	id := c.state.NextID
	im := IndexMeta{Name: name, Column: column, File: fileName(id)}

	updated := tm.clone()
	updated.Indexes = append(updated.Indexes, im)
	c.state.Tables[table] = updated
	c.state.NextID = id + 1
	if err := c.persist(); err != nil {
		c.state.Tables[table] = tm // restore the pre-append value
		c.state.NextID = id
		return IndexMeta{}, err
	}
	return im, nil
}

// GetTable returns a copy of the named table's metadata and whether it exists.
func (c *Catalog) GetTable(name string) (TableMeta, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tm, ok := c.state.Tables[name]
	if !ok {
		return TableMeta{}, false
	}
	return tm.clone(), true
}

// ListTables returns copies of every table's metadata, ordered by name.
func (c *Catalog) ListTables() []TableMeta {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]TableMeta, 0, len(c.state.Tables))
	for _, tm := range c.state.Tables {
		out = append(out, tm.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// persist serializes the current state and hands it to the store. The state is
// always JSON-encodable (strings, ints, slices, a string-keyed map), so the
// encode cannot fail — only the store's write can.
func (c *Catalog) persist() error {
	data, _ := json.MarshalIndent(c.state, "", "  ")
	if err := c.store.save(data); err != nil {
		return fmt.Errorf("catalog: save: %w", err)
	}
	return nil
}

// fileName is the counter-based name for a relation's file (PostgreSQL
// relfilenode style): stable under table/index renames.
func fileName(id uint32) string { return fmt.Sprintf("rel_%d.db", id) }
