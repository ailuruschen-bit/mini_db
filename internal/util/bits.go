// Package util provides low-level helpers shared across the engine.
//
// The bit helpers read and write a run of bits at an arbitrary bit offset
// inside a byte slice, treating the slice as one big-endian (MSB-first) bit
// stream. They generalise the hand-written mask/shift logic used by the packed
// on-disk structures (slot entries, tuple headers).
package util

import "fmt"

// maxBitLength is the widest run these helpers handle: values are carried in a
// uint16, so at most 16 bits.
const maxBitLength = 16

// ReadBits returns the `length`-bit value that starts at bit `offset` (counted
// MSB-first from the start of bytes). length must be in [1, 16].
func ReadBits(bytes []byte, offset uint16, length byte) (uint16, error) {
	if length == 0 || length > maxBitLength {
		return 0, fmt.Errorf("length %d out of range [1,%d]", length, maxBitLength)
	}
	if uint32(offset)+uint32(length) > uint32(len(bytes))*8 {
		return 0, fmt.Errorf("read range [%d,%d) exceeds %d bits", offset, uint32(offset)+uint32(length), len(bytes)*8)
	}

	// bytes spanned by the target bits: [startByte, endByte]
	startByte := offset / 8
	endByte := (offset + uint16(length) - 1) / 8
	window := bytes[startByte : endByte+1]

	// bits sitting after the target inside the window
	tail := uint16(len(window))*8 - offset%8 - uint16(length)

	// pack the window (up to 3 bytes) into a uint32, MSB first
	var windowVal uint32
	for _, b := range window {
		windowVal = windowVal<<8 | uint32(b)
	}

	mask := uint32(1)<<length - 1
	return uint16(windowVal >> tail & mask), nil
}

// WriteBits stores the low `length` bits of val at bit `offset` (MSB-first),
// leaving every other bit in bytes untouched. length must be in [1, 16] and val
// must fit in `length` bits.
func WriteBits(bytes []byte, offset uint16, length byte, val uint16) (bool, error) {
	if length == 0 || length > maxBitLength {
		return false, fmt.Errorf("length %d out of range [1,%d]", length, maxBitLength)
	}
	if uint32(offset)+uint32(length) > uint32(len(bytes))*8 {
		return false, fmt.Errorf("write range [%d,%d) exceeds %d bits", offset, uint32(offset)+uint32(length), len(bytes)*8)
	}
	if uint32(val) > uint32(1)<<length-1 {
		return false, fmt.Errorf("value %d does not fit in %d bits", val, length)
	}

	startByte := offset / 8
	endByte := (offset + uint16(length) - 1) / 8
	window := bytes[startByte : endByte+1]

	tail := uint16(len(window))*8 - offset%8 - uint16(length)

	var windowVal uint32
	for _, b := range window {
		windowVal = windowVal<<8 | uint32(b)
	}

	// clear the target bits, then merge the new value in
	mask := (uint32(1)<<length - 1) << tail
	windowVal = windowVal&^mask | uint32(val)<<tail

	// write the window back, MSB first
	for i := len(window) - 1; i >= 0; i-- {
		window[i] = byte(windowVal)
		windowVal >>= 8
	}
	return true, nil
}
