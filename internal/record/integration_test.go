package record_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/access/heap"
	"github.com/ailuruschen-bit/minidb/internal/record"
	"github.com/ailuruschen-bit/minidb/internal/storage/buffer"
	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
)

// The full stack: a typed row is encoded, stored in a heap file through the
// buffer pool, read back by its TID, and decoded to the same row.
func TestRecordHeapIntegration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rows.db")
	dm, err := disk.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dm.Close() }()
	h := heap.NewHeap(buffer.NewBufferPool(8).Register(dm))
	s := sampleSchema()

	want := []record.Row{
		{int64(100), "carol", "hi"},
		{int64(200), "dave", nil}, // with a NULL
		{int64(-7), "", "z"},
	}
	tids := make([]heap.TID, len(want))
	for i, row := range want {
		b, err := s.Encode(row)
		if err != nil {
			t.Fatalf("encode %v: %v", row, err)
		}
		tid, err := h.Insert(b)
		if err != nil {
			t.Fatalf("insert %v: %v", row, err)
		}
		tids[i] = tid
	}

	for i, tid := range tids {
		stored, err := h.Get(tid)
		if err != nil {
			t.Fatalf("get %+v: %v", tid, err)
		}
		got, err := s.Decode(stored)
		if err != nil {
			t.Fatalf("decode %+v: %v", tid, err)
		}
		if !reflect.DeepEqual([]any(got), []any(want[i])) {
			t.Errorf("row %d round-trip = %#v, want %#v", i, got, want[i])
		}
	}
}
