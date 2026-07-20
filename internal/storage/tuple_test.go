package storage

import (
	"bytes"
	"math"
	"testing"
)

// tupleField describes one field of the 12-byte tuple header. The header mixes
// two kinds of field, so the descriptor normalises them:
//   - whole-byte fields (t_xmin, t_xmax, t_hoff) use their full type width and
//     their setters cannot fail, so they are wrapped to report success
//   - packed fields (flags, col_count) share [8:10] and their setters validate
//     the range, reporting (false, error) on overflow
type tupleField struct {
	name      string
	max       uint64
	validated bool // setter rejects values above max
	set       func(h *TupleHeader, v uint64) (bool, error)
	get       func(h *TupleHeader) uint64
}

func tupleFields() []tupleField {
	return []tupleField{
		{"t_xmin", math.MaxUint32, false,
			func(h *TupleHeader, v uint64) (bool, error) { h.SetTxMin(uint32(v)); return true, nil },
			func(h *TupleHeader) uint64 { return uint64(h.TxMin()) }},
		{"t_xmax", math.MaxUint32, false,
			func(h *TupleHeader, v uint64) (bool, error) { h.SetTxMax(uint32(v)); return true, nil },
			func(h *TupleHeader) uint64 { return uint64(h.TxMax()) }},
		{"flags", uint64(maxVal6), true,
			func(h *TupleHeader, v uint64) (bool, error) { return h.SetFlags(uint8(v)) },
			func(h *TupleHeader) uint64 { return uint64(h.Flags()) }},
		{"col_count", uint64(maxVal10), true,
			func(h *TupleHeader, v uint64) (bool, error) { return h.SetColumnCount(uint16(v)) },
			func(h *TupleHeader) uint64 { return uint64(h.ColumnCount()) }},
		{"t_hoff", math.MaxUint8, false,
			func(h *TupleHeader, v uint64) (bool, error) { h.SetHoff(uint8(v)); return true, nil },
			func(h *TupleHeader) uint64 { return uint64(h.Hoff()) }},
	}
}

// Round-trip + boundaries.
func TestTupleHeaderRoundTrip(t *testing.T) {
	for _, f := range tupleFields() {
		t.Run(f.name, func(t *testing.T) {
			h := blankTupleHeader()
			for _, v := range []uint64{0, 1, f.max} {
				ok, err := f.set(h, v)
				mustSet(t, f.name, ok, err)
				if got := f.get(h); got != v {
					t.Errorf("set %d, got %d", v, got)
				}
			}
		})
	}
}

// Field independence across both backgrounds. Covers the packed pair
// flags/col_count sharing [8:10] as well as the whole-byte neighbours.
func TestTupleHeaderFieldIndependence(t *testing.T) {
	fields := tupleFields()

	directions := []struct {
		name string
		bg   func(f tupleField) uint64
		poke func(f tupleField) uint64
	}{
		{"zero-background",
			func(tupleField) uint64 { return 0 },
			func(f tupleField) uint64 { return f.max }},
		{"max-background",
			func(f tupleField) uint64 { return f.max },
			func(tupleField) uint64 { return 0 }},
	}

	for _, d := range directions {
		for _, target := range fields {
			t.Run(d.name+"/"+target.name, func(t *testing.T) {
				h := blankTupleHeader()
				for _, f := range fields {
					ok, err := f.set(h, d.bg(f))
					mustSet(t, f.name, ok, err)
				}
				ok, err := target.set(h, d.poke(target))
				mustSet(t, target.name, ok, err)

				for _, f := range fields {
					want := d.bg(f)
					if f.name == target.name {
						want = d.poke(f)
					}
					if got := f.get(h); got != want {
						t.Errorf("after writing %s: field %s = %d, want %d", target.name, f.name, got, want)
					}
				}
			})
		}
	}
}

// Error path, for the packed fields whose setters validate their range.
func TestTupleHeaderRejectsOverflow(t *testing.T) {
	for _, f := range tupleFields() {
		if !f.validated {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			h := blankTupleHeader()

			const seed = 1
			ok, err := f.set(h, seed)
			mustSet(t, f.name, ok, err)

			ok, err = f.set(h, f.max+1)
			if ok || err == nil {
				t.Errorf("set(%d) succeeded (ok=%v, err=%v), want rejection", f.max+1, ok, err)
			}
			if got := f.get(h); got != seed {
				t.Errorf("rejected write modified the field: got %d, want %d", got, seed)
			}
		})
	}
}

// Golden layout for the whole 12-byte header.
//
//	t_xmin    = 0xDEADBEEF          -> DE AD BE EF
//	t_xmax    = 0x01020304          -> 01 02 03 04
//	flags     = 0x2A (6b)  << 10 \
//	col_count = 0x155 (10b)       > -> A9 55
//	t_hoff    = 14                  -> 0E
//	reserved                        -> 00
func TestTupleHeaderGoldenLayout(t *testing.T) {
	h := blankTupleHeader()

	h.SetTxMin(0xDEADBEEF)
	h.SetTxMax(0x01020304)
	ok, err := h.SetFlags(0x2A)
	mustSet(t, "flags", ok, err)
	ok, err = h.SetColumnCount(0x155)
	mustSet(t, "col_count", ok, err)
	h.SetHoff(14)

	want := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04, 0xA9, 0x55, 0x0E, 0x00}
	if got := h.data[:]; !bytes.Equal(got, want) {
		t.Errorf("raw bytes = % x, want % x", got, want)
	}
}

// The reserved byte [11] must stay zero no matter what the setters write.
func TestTupleHeaderReservedByteUntouched(t *testing.T) {
	h := blankTupleHeader()
	for _, f := range tupleFields() {
		ok, err := f.set(h, f.max)
		mustSet(t, f.name, ok, err)
	}
	if got := h.data[11]; got != 0 {
		t.Errorf("reserved byte [11] = %#x, want 0", got)
	}
}

// HasNull is a convenience read of the FlagHasNull hint bit.
func TestTupleHeaderHasNull(t *testing.T) {
	tests := []struct {
		name  string
		flags uint8
		want  bool
	}{
		{"no flags", 0, false},
		{"only has-null", FlagHasNull, true},
		{"other bit only", FlagXminCommitted, false},
		{"has-null among others", FlagHasNull | FlagXmaxAborted, true},
		{"all bits", maxVal6, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := blankTupleHeader()
			ok, err := h.SetFlags(tt.flags)
			mustSet(t, "flags", ok, err)
			if got := h.HasNull(); got != tt.want {
				t.Errorf("flags=%#b HasNull()=%v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

// TupleHeader() must be a view over exactly the tuple's first 12 bytes:
// writes through it land in the tuple, and nothing past byte 12 is touched.
func TestTupleHeaderViewAliasesTuple(t *testing.T) {
	const total = 20
	tup := blankTuple(total)
	h := tup.TupleHeader()

	h.SetTxMin(math.MaxUint32)
	h.SetHoff(math.MaxUint8)

	if got := tup.data[0:4]; !bytes.Equal(got, []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("t_xmin did not write through to the tuple: % x", got)
	}
	if tup.data[10] != 0xFF {
		t.Errorf("t_hoff did not write through to the tuple: %#x", tup.data[10])
	}
	for i := int(TupleHeaderSize); i < total; i++ {
		if tup.data[i] != 0 {
			t.Errorf("byte %d past the header was modified: %#x", i, tup.data[i])
		}
	}
}
