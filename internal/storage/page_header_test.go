package storage

import (
	"bytes"
	"math"
	"testing"
)

// headerField lets a single table drive every PageHeader field uniformly.
// Each value is carried as a uint64 regardless of the field's real width, and
// set/get are closures over the concrete typed accessors.
type headerField struct {
	name   string
	offset int
	size   int
	max    uint64
	set    func(h *PageHeader, v uint64)
	get    func(h *PageHeader) uint64
}

func headerFields() []headerField {
	return []headerField{
		{"pd_lsn", 0, 8, math.MaxUint64,
			func(h *PageHeader, v uint64) { h.SetPdLsn(v) },
			func(h *PageHeader) uint64 { return h.PdLsn() }},
		{"pd_checksum", 8, 2, math.MaxUint16,
			func(h *PageHeader, v uint64) { h.SetPdChecksum(uint16(v)) },
			func(h *PageHeader) uint64 { return uint64(h.PdChecksum()) }},
		{"pd_flags", 10, 2, math.MaxUint16,
			func(h *PageHeader, v uint64) { h.SetPdFlags(uint16(v)) },
			func(h *PageHeader) uint64 { return uint64(h.PdFlags()) }},
		{"pd_lower", 12, 2, math.MaxUint16,
			func(h *PageHeader, v uint64) { h.SetPdLower(uint16(v)) },
			func(h *PageHeader) uint64 { return uint64(h.PdLower()) }},
		{"pd_upper", 14, 2, math.MaxUint16,
			func(h *PageHeader, v uint64) { h.SetPdUpper(uint16(v)) },
			func(h *PageHeader) uint64 { return uint64(h.PdUpper()) }},
		{"pd_special", 16, 2, math.MaxUint16,
			func(h *PageHeader, v uint64) { h.SetPdSpecial(uint16(v)) },
			func(h *PageHeader) uint64 { return uint64(h.PdSpecial()) }},
		{"pd_pagesize", 18, 2, math.MaxUint16,
			func(h *PageHeader, v uint64) { h.SetPdPagesize(uint16(v)) },
			func(h *PageHeader) uint64 { return uint64(h.PdPagesize()) }},
		{"pd_prune_xid", 20, 4, math.MaxUint32,
			func(h *PageHeader, v uint64) { h.SetPdPruneXid(uint32(v)) },
			func(h *PageHeader) uint64 { return uint64(h.PdPruneXid()) }},
	}
}

// Round-trip + boundaries: set then get returns the same value, for 0, 1 and
// the field maximum.
func TestPageHeaderRoundTrip(t *testing.T) {
	for _, f := range headerFields() {
		t.Run(f.name, func(t *testing.T) {
			h := blankPage().Header()
			for _, v := range []uint64{0, 1, f.max} {
				f.set(h, v)
				if got := f.get(h); got != v {
					t.Errorf("set %d, got %d", v, got)
				}
			}
		})
	}
}

// Golden layout: after a set, the raw bytes at the field's range hold the
// big-endian encoding. Catches wrong offset / wrong byte order, which a
// get/set pair sharing the same wrong offset would hide.
func TestPageHeaderGoldenLayout(t *testing.T) {
	for _, f := range headerFields() {
		t.Run(f.name, func(t *testing.T) {
			h := blankPage().Header()
			f.set(h, f.max)
			got := h.data[f.offset : f.offset+f.size]
			want := beBytes(f.max, f.size)
			if !bytes.Equal(got, want) {
				t.Errorf("raw bytes at [%d:%d] = %x, want %x", f.offset, f.offset+f.size, got, want)
			}
		})
	}
}

// Field isolation: fill every field with its max marker, zero one, and verify
// no neighbour changed. Detects overlapping / off-by-one offsets.
func TestPageHeaderFieldIsolation(t *testing.T) {
	fields := headerFields()
	for _, target := range fields {
		t.Run(target.name, func(t *testing.T) {
			h := blankPage().Header()
			for _, f := range fields {
				f.set(h, f.max)
			}
			target.set(h, 0)
			for _, f := range fields {
				want := f.max
				if f.name == target.name {
					want = 0
				}
				if got := f.get(h); got != want {
					t.Errorf("after zeroing %s: field %s = %d, want %d", target.name, f.name, got, want)
				}
			}
		})
	}
}
