package storage

import (
	"encoding/binary"

	"github.com/ailuruschen-bit/minidb/internal/util"
)

// Field maxima, kept as the single source of truth for each packed field's
// upper bound. Production code no longer needs them (util.WriteBits validates
// range), but the tests reference them as the field's documented maximum.
const (
	maxVal6  uint8  = (1 << 6) - 1
	maxVal10 uint16 = (1 << 10) - 1
)

// Bit layout of flags + col_count, packed into bytes [8:10] of the header, as
// (bit position, bit length) pairs counted MSB-first from the start of the
// header: flags occupies the high 6 bits, col_count the low 10.
const (
	flagsBitPos, flagsBitLen       = 8 * 8, 6                      // high 6 bits of [8:10]
	colCountBitPos, colCountBitLen = flagsBitPos + flagsBitLen, 10 // low 10 bits of [8:10]
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
//
// Getters of packed fields ignore ReadBits' error: the position/length are
// compile-time constants known to be in range, so it can never fail here.

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
	v, _ := util.ReadBits(h.data[:], flagsBitPos, flagsBitLen)
	return uint8(v)
}

func (h *TupleHeader) SetFlags(val uint8) (bool, error) {
	return util.WriteBits(h.data[:], flagsBitPos, flagsBitLen, uint16(val))
}

// HasNull reports whether a null bitmap follows the header.
func (h *TupleHeader) HasNull() bool {
	return h.Flags()&FlagHasNull != 0
}

// ColumnCount: number of columns (low 10 bit of [8:10])
func (h *TupleHeader) ColumnCount() uint16 {
	v, _ := util.ReadBits(h.data[:], colCountBitPos, colCountBitLen)
	return v
}

func (h *TupleHeader) SetColumnCount(val uint16) (bool, error) {
	return util.WriteBits(h.data[:], colCountBitPos, colCountBitLen, val)
}

// Hoff (t_hoff): offset from tuple start to column data (1 byte)
func (h *TupleHeader) Hoff() uint8 {
	return h.data[10]
}

func (h *TupleHeader) SetHoff(off uint8) {
	h.data[10] = off
}
