package storage

import (
	"fmt"
	"iter"
)

const (
	PageSize        uint16 = 8192
	HeaderSize      uint16 = 24
	SlotEntrySize   uint16 = 4
	TupleHeaderSize uint16 = 12
)

// noCopy triggers go vet's copylock check when a value embedding it is copied
// by value. It implements sync.Locker but does nothing at runtime and is
// zero-sized, so it adds neither behavior nor memory overhead.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// === SlottedPage Define ===
//
// SlottedPage owns an 8KB backing array; Header()/SlotEntryAt()/Tuple all hand
// out views into that array. Copying a SlottedPage by value would
// detach those views from the copy, so it must only be passed by pointer.
// The embedded noCopy makes `go vet` flag any accidental value copy.
type SlottedPage struct {
	_    noCopy
	data [PageSize]byte
}

// --- Core Function ---

// --- All Page Filed (Object Getter + Setter) ---

// Page Header: Metadata of Page
func (p *SlottedPage) Header() *PageHeader {
	return &PageHeader{(*[HeaderSize]byte)(p.data[:HeaderSize])}
}

// SlotCount reports how many entries the slot directory holds. The directory
// spans [HeaderSize, pd_upper), so pd_upper alone determines the count.
func (p *SlottedPage) SlotCount() uint16 {
	return (p.Header().PdUpper() - HeaderSize) / SlotEntrySize
}

// SlotEntryAt returns a view over slot i.
//
// The directory is already a contiguous array of fixed-size entries inside the
// page, so a slot's position is pure arithmetic; no intermediate index is
// built. The returned value is a copy, but it carries a pointer into the page's
// backing array, so writing through it updates the page.
//
// Panics if i is out of range, like any other index expression in Go.
func (p *SlottedPage) SlotEntryAt(i uint16) SlotEntry {
	if n := p.SlotCount(); i >= n {
		panic(fmt.Sprintf("storage: slot index %d out of range [0,%d)", i, n))
	}
	return p.slotEntryAt(i)
}

// slotEntryAt is the unchecked form. Callers must already know i is in range;
// it exists so that iteration does not re-derive the bound on every element.
func (p *SlottedPage) slotEntryAt(i uint16) SlotEntry {
	at := HeaderSize + i*SlotEntrySize
	return SlotEntry{(*[SlotEntrySize]byte)(p.data[at : at+SlotEntrySize])}
}

// Slots iterates the slot directory in order without allocating. Prefer it
// over collecting the entries into a slice.
func (p *SlottedPage) Slots() iter.Seq2[uint16, SlotEntry] {
	return func(yield func(uint16, SlotEntry) bool) {
		n := p.SlotCount() // the loop guarantees the bound, so skip the check below
		for i := uint16(0); i < n; i++ {
			if !yield(i, p.slotEntryAt(i)) {
				return
			}
		}
	}
}

// Find Tuple by the pointer val (from an Entry)
func (p *SlottedPage) LocateTupleByEntry(entry *SlotEntry) *Tuple {
	return &Tuple{p.data[entry.Offset() : entry.Offset()+entry.Length()]}
}

// --- Tool Function ---
func NewSlottedPage(data [PageSize]byte) *SlottedPage {
	return &SlottedPage{data: data}
}
