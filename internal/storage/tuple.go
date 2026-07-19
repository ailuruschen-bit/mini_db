package storage

import (
	"encoding/binary"
	"fmt"
)

const (
	maxVal6  uint8  = (1 << 6) - 1
	maxVal10 uint16 = (1 << 10) - 1
)

// MVCC hint bits packed into the 6-bit flags field of the tuple header.
// They cache the commit/abort status of t_xmin / t_xmax so that visibility
// checks can avoid a lookup into the transaction status log (CLOG).
const (
	FlagHasNull       uint8 = 1 << 0 // a null bitmap follows the header
	FlagXminCommitted uint8 = 1 << 1 // inserting transaction known committed
	FlagXminAborted   uint8 = 1 << 2 // inserting transaction known aborted
	FlagXmaxCommitted uint8 = 1 << 3 // deleting transaction known committed
	FlagXmaxAborted   uint8 = 1 << 4 // deleting transaction known aborted
	// bit 5 reserved
)

// === Tuple Define ===
// Layout: TupleHeader (12 byte) -> null_bitmap (optional) -> column data.
type Tuple struct {
	data []byte
}

func (t *Tuple) TupleHeader() *TupleHeader {
	return &TupleHeader{(*[TupleHeaderSize]byte)(t.data[:TupleHeaderSize])}
}

// === Tuple Header Define (12 byte) ===
type TupleHeader struct {
	data *[TupleHeaderSize]byte
}

// --- All Header Fields (Value Getter + Setter) ---

// t_xmin: id of the transaction that inserted the tuple (4 byte)
func (h *TupleHeader) TxMin() uint32 {
	return binary.BigEndian.Uint32(h.data[0:4])
}

func (h *TupleHeader) SetTxMin(xid uint32) {
	binary.BigEndian.PutUint32(h.data[0:4], xid)
}

// t_xmax: id of the transaction that deleted the tuple; 0 means live (4 byte)
func (h *TupleHeader) TxMax() uint32 {
	return binary.BigEndian.Uint32(h.data[4:8])
}

func (h *TupleHeader) SetTxMax(xid uint32) {
	binary.BigEndian.PutUint32(h.data[4:8], xid)
}

// Flags: MVCC hint bits (high 6 bit of [8:10])
func (h *TupleHeader) Flags() uint8 {
	return uint8(binary.BigEndian.Uint16(h.data[8:10]) >> 10)
}

func (h *TupleHeader) SetFlags(val uint8) (bool, error) {
	if val > maxVal6 {
		return false, fmt.Errorf("value exceeds maximum allowed (%d)", maxVal6)
	}

	bitWindow := h.data[8:10]
	windowVal := binary.BigEndian.Uint16(bitWindow)
	// keep low 10 bit (col_count), replace high 6 bit (flags)
	windowVal = (windowVal & maxVal10) | (uint16(val) << 10)
	binary.BigEndian.PutUint16(bitWindow, windowVal)
	return true, nil
}

// HasNull reports whether a null bitmap follows the header.
func (h *TupleHeader) HasNull() bool {
	return h.Flags()&FlagHasNull != 0
}

// ColumnCount: number of columns (low 10 bit of [8:10])
func (h *TupleHeader) ColumnCount() uint16 {
	return binary.BigEndian.Uint16(h.data[8:10]) & maxVal10
}

func (h *TupleHeader) SetColumnCount(val uint16) (bool, error) {
	if val > maxVal10 {
		return false, fmt.Errorf("value exceeds maximum allowed (%d)", maxVal10)
	}

	bitWindow := h.data[8:10]
	windowVal := binary.BigEndian.Uint16(bitWindow)
	// keep high 6 bit (flags), replace low 10 bit (col_count)
	windowVal = (windowVal &^ maxVal10) | val
	binary.BigEndian.PutUint16(bitWindow, windowVal)
	return true, nil
}

// Hoff (t_hoff): offset from tuple start to column data (1 byte)
func (h *TupleHeader) Hoff() uint8 {
	return h.data[10]
}

func (h *TupleHeader) SetHoff(off uint8) {
	h.data[10] = off
}
