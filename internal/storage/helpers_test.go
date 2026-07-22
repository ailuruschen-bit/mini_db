package storage

import "encoding/binary"

// blankPage returns a heap-allocated, zero-filled page. Because the page lives
// on the heap and is used through the returned pointer, every view obtained
// from it (Header, SlotEntry, Tuple) shares the same backing array.
func blankPage() *SlottedPage {
	return NewSlottedPage([PageSize]byte{})
}

// blankSlotEntry returns a standalone, zero-filled slot entry. Bit-packing can
// be exercised without building a whole page around it.
func blankSlotEntry() *SlotEntry {
	var buf [SlotEntrySize]byte
	return &SlotEntry{&buf}
}

// blankTupleHeader returns a standalone, zero-filled tuple header.
func blankTupleHeader() *TupleHeader {
	var buf [TupleHeaderSize]byte
	return &TupleHeader{&buf}
}

// blankTuple returns a zero-filled tuple of the given total byte length.
func blankTuple(size int) *Tuple {
	return &Tuple{make([]byte, size)}
}

// beBytes returns the big-endian encoding of v kept to its low `size` bytes.
// Used by golden-layout tests to compute the expected raw bytes of a field.
func beBytes(v uint64, size int) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[8-size:]
}
