package storage

import (
	"encoding/binary"
	"fmt"
)

const (
	maxVal15 uint16 = (1 << 15) - 1
	maxVal2  byte   = (1 << 2) - 1
)

// === Slot Entry Define (4 byte) ===
type SlotEntry struct {
	data *[SlotEntrySize]byte
}

// --- All Entry Fields ---

// Offset: point to specific slot top (15 bit)
func (s *SlotEntry) Offset() uint16 {
	bitWindow := s.data[0:2]
	windowVal := binary.BigEndian.Uint16(bitWindow)
	// first 15bit
	val := windowVal >> 1

	return val
}

func (s *SlotEntry) SetOffset(val uint16) (bool, error) {
	if val > maxVal15 {
		return false, fmt.Errorf("value exceeds maximum allowed (%d)", maxVal15)
	}

	bitWindow := s.data[0:2]
	windowVal := binary.BigEndian.Uint16(bitWindow)
	// set first 15bit to 0
	mask := uint16(1)
	windowVal = windowVal & mask
	// merge new value to first 15bit
	merge := val << 1
	windowVal = windowVal | merge
	// write into []byte
	binary.BigEndian.PutUint16(bitWindow, windowVal)
	return true, nil
}

// Length: the length of slot (15 bit)
func (s *SlotEntry) Length() uint16 {
	bitWindow := s.data[0:]
	bitVal := binary.BigEndian.Uint32(bitWindow)
	// from 16 to 30 in 32bit
	mask := uint32(((1 << 15) - 1) << 2)
	val := uint16((bitVal & mask) >> 2)

	return val
}

func (s *SlotEntry) SetLength(val uint16) (bool, error) {
	if val > maxVal15 {
		return false, fmt.Errorf("value exceeds maximum allowed (%d)", maxVal15)
	}

	bitWindow := s.data[0:]
	windowVal := binary.BigEndian.Uint32(bitWindow)
	// set first 16 ~ 30 bit to 0
	mask := uint32(maxVal15) << 2
	windowVal = windowVal &^ mask
	// merge new value to 16 ~ 30 bit
	merge := uint32(val) << 2
	windowVal = windowVal | merge
	// write into []byte
	binary.BigEndian.PutUint32(bitWindow, windowVal)
	return true, nil
}

// Flag: the status of slot (2 bit)
func (s *SlotEntry) Flag() byte {
	bitWindow := s.data[2:]
	bitVal := binary.BigEndian.Uint16(bitWindow)
	// last 2 bit
	mask := uint16((1 << 2) - 1)
	val := byte(bitVal & mask)

	return val
}

func (s *SlotEntry) SetFlag(val byte) (bool, error) {
	if val > maxVal2 {
		return false, fmt.Errorf("value exceeds maximum allowed (%d)", maxVal15)
	}

	bitWindow := s.data[2:]
	windowVal := binary.BigEndian.Uint16(bitWindow)
	// set last 2 bit to 0
	windowVal = windowVal &^ uint16(maxVal2)
	// merge
	windowVal = windowVal | uint16(val)
	// write into []byte
	binary.BigEndian.PutUint16(bitWindow, windowVal)
	return true, nil
}

// --- Tool Functions ---
