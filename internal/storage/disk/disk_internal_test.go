package disk

// White-box tests: the black-box suite in disk_test.go covers the public
// contract, but a couple of guards can only be reached by touching unexported
// state directly.

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The page counter is a uint32. Allocating past its maximum would wrap it to 0,
// which would silently make every existing page unreachable and let the next
// allocation overwrite page 0. The guard is unreachable in practice (2^32 pages
// is 32 TiB), so it is exercised by setting the counter directly.
func TestAllocatePageRefusesPastMaxPageID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	d.numPages = math.MaxUint32

	if _, err := d.AllocatePage(); err == nil {
		t.Error("AllocatePage past the maximum page id did not fail")
	}
	if got := d.numPages; got != math.MaxUint32 {
		t.Errorf("numPages changed after the refusal: %d", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("file was extended despite the refusal: size %d", info.Size())
	}
}
