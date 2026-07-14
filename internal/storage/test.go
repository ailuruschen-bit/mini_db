package storage

import (
	"encoding/binary"
)

// === Test Functions===
func (p SlottedPage) PdLowerFromPage() uint16 {
	return binary.LittleEndian.Uint16(p.data[12:14])
}

func (h PageHeader) PdLowerFromHeader() uint16 {
	return binary.LittleEndian.Uint16(h.data[12:14])
}
