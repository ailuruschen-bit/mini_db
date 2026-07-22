package storage

import (
	"github.com/ailuruschen-bit/minidb/internal/util"
)

// Bit layout of the 4-byte slot entry, as (bit position, bit length) pairs
// counted MSB-first from the start of the entry. Laid out contiguously:
// offset [0,15) | length [15,30) | flag [30,32).
const (
	offsetBitPos, offsetBitLen = 0, 15
	lengthBitPos, lengthBitLen = offsetBitPos + offsetBitLen, 15
	flagBitPos, flagBitLen     = lengthBitPos + lengthBitLen, 2
)

// === Slot Entry Define (4 byte) ===
//
// A SlotEntry is a view: it holds nothing but a pointer into a page's backing
// array, so copying an entry copies that pointer and every setter still writes
// through to the page.
//
// Methods use pointer receivers, matching the other view types in this package.
// SlotEntryAt and Slots hand out entries by value (returning a pointer would
// force a heap allocation per entry), so a caller must bind one to a variable
// before calling a method on it — a function result is not addressable:
//
//	e := p.SlotEntryAt(i)
//	e.Offset()
type SlotEntry struct {
	data *[SlotEntrySize]byte
}

// --- All Entry Fields ---
//
// Getters ignore ReadBits' error: the position/length are compile-time
// constants known to be in range, so it can never fail here.

// Offset: points to the start of the tuple within the page (15 bit).
func (s *SlotEntry) Offset() uint16 {
	v, _ := util.ReadBits(s.data[:], offsetBitPos, offsetBitLen)
	return v
}

func (s *SlotEntry) SetOffset(val uint16) (bool, error) {
	return util.WriteBits(s.data[:], offsetBitPos, offsetBitLen, val)
}

// Length: byte length of the tuple this slot points to (15 bit).
func (s *SlotEntry) Length() uint16 {
	v, _ := util.ReadBits(s.data[:], lengthBitPos, lengthBitLen)
	return v
}

func (s *SlotEntry) SetLength(val uint16) (bool, error) {
	return util.WriteBits(s.data[:], lengthBitPos, lengthBitLen, val)
}

// Flag: the status of the slot (2 bit).
func (s *SlotEntry) Flag() byte {
	v, _ := util.ReadBits(s.data[:], flagBitPos, flagBitLen)
	return byte(v)
}

func (s *SlotEntry) SetFlag(val byte) (bool, error) {
	return util.WriteBits(s.data[:], flagBitPos, flagBitLen, uint16(val))
}

// --- Tool Functions ---
