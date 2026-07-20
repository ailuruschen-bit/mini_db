package storage

import "encoding/binary"

// blankPage returns a heap-allocated, zero-filled page. Because the page lives
// on the heap and is used through the returned pointer, every view obtained
// from it (Header, SlotEntry, Tuple) shares the same backing array.
func blankPage() *SlottedPage {
	return NewSlottedPage([PageSize]byte{})
}

// beBytes returns the big-endian encoding of v kept to its low `size` bytes.
// Used by golden-layout tests to compute the expected raw bytes of a field.
func beBytes(v uint64, size int) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[8-size:]
}
