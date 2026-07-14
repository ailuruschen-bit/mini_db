package storage

import "encoding/binary"

type Tuple struct {
	data []byte
}

// --- Tuple Header ---
type TupleHeader struct {
	data *[TupleHeaderSize]byte
}

func (t *Tuple) TupleHeader() *TupleHeader {
	return &TupleHeader{(*[TupleHeaderSize]byte)(t.data[:TupleHeaderSize])}
}

// Transaction ID (4 byte)
func (h *TupleHeader) TransactionId() uint32 {
	// TODO: not implemented yet
	return 0
}

// tuple size (2 byte)
func (h *TupleHeader) Size() uint16 {
	return binary.BigEndian.Uint16(h.data[2:4])
}

// Status Flag (6 bit)
func (h *TupleHeader) StatusFlag() uint8 {
	return uint8(binary.BigEndian.Uint16(h.data[4:6]) >> 10)
}

// column number (10 bit)
func (h *TupleHeader) ColumnNumber() uint16 {
	return binary.BigEndian.Uint16(h.data[4:6]) & ((1 << 10) - 1)
}
