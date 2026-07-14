package util

import (
	"fmt"
)

// retrieve info from exact bit offset
func readByBitOffset(bytes []byte, offset uint16, length byte) (uint16, error) {
	// length validate
	if length == 0 || length > 16 {
		return 0, fmt.Errorf("length %d exceeds maximum allowed (16 bit)", length)
	}
	if uint32(offset)+uint32(length) > uint32(len(bytes))*8 {
		return 0, fmt.Errorf("read range out of bounds")
	}

	// index of the byte where the first and last bit in
	startInOrigin := offset / 8
	endInOrigin := (offset + uint16(length) - 1) / 8

	// the window surround all target bits
	bitWindow := bytes[startInOrigin : endInOrigin+1]
	// the extra tail length (bits after the target inside the window)
	tailLength := uint16(len(bitWindow))*8 - offset%8 - uint16(length)

	// transform the window from []byte to uint32 (MSB first)
	var windowVal uint32
	for i := 0; i < len(bitWindow); i++ {
		windowVal = windowVal<<8 | uint32(bitWindow[i])
	}

	// cut the target value from window
	mask := uint32(1)<<length - 1
	targetVal := windowVal >> tailLength & mask

	return uint16(targetVal), nil
}

// write info to exact bit offset
func setByBitOffset(bytes []byte, offset uint16, length byte, val uint16) (bool, error) {
	// length validate
	if length == 0 || length > 16 {
		return false, fmt.Errorf("length %d exceeds maximum allowed (16 bit)", length)
	}
	if uint32(offset)+uint32(length) > uint32(len(bytes))*8 {
		return false, fmt.Errorf("write range out of bounds")
	}
	if uint32(val) > uint32(1)<<length-1 {
		return false, fmt.Errorf("write out of bounds")
	}

	// index of the byte where the first and last bit in
	startInOrigin := offset / 8
	endInOrigin := (offset + uint16(length) - 1) / 8

	// the window surround all target bits
	bitWindow := bytes[startInOrigin : endInOrigin+1]
	// the extra tail length
	tailLength := uint16(len(bitWindow))*8 - offset%8 - uint16(length)

	// transform the window from []byte to uint32 (MSB first)
	var windowVal uint32
	for i := 0; i < len(bitWindow); i++ {
		windowVal = windowVal<<8 | uint32(bitWindow[i])
	}

	// clear the target bits, then merge new value
	mask := (uint32(1)<<length - 1) << tailLength
	windowVal = windowVal&^mask | uint32(val)<<tailLength

	// write the window back to the bytes (MSB first)
	for i := len(bitWindow) - 1; i >= 0; i-- {
		bitWindow[i] = byte(windowVal)
		windowVal >>= 8
	}
	return true, nil
}
