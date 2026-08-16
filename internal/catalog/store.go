package catalog

import (
	"errors"
	"os"
)

// blobStore persists the catalog's serialized bytes. It is split out from the
// catalog so tests can inject load/save failures that the os cannot easily be
// made to produce, the way the buffer pool injects a fakeDisk.
type blobStore interface {
	// load returns the stored bytes and true, or (nil, false, nil) when nothing
	// has been stored yet.
	load() (data []byte, ok bool, err error)
	// save durably replaces the stored bytes.
	save(data []byte) error
}

// fileStore persists the catalog to a single JSON file.
type fileStore struct{ path string }

func (s fileStore) load() ([]byte, bool, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// save writes to a temp file and renames it over the target, so a crash
// mid-write cannot leave a half-written catalog behind.
func (s fileStore) save(data []byte) error {
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
