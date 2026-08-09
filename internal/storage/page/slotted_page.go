package page

import (
	"errors"
	"fmt"
	"iter"
)

// ErrNoSpace is returned by InsertTuple when the free space cannot hold the
// tuple plus its slot. It is not a corruption — the caller (the heap layer)
// uses it to move on to another page or allocate a new one.
var ErrNoSpace = errors.New("page: not enough free space for the tuple")

// Layout constants for the slotted-page format. PageSize (the frame size) lives
// with the Page abstraction in page.go, since it is not specific to this layout.
const (
	HeaderSize      uint16 = 24
	SlotEntrySize   uint16 = 4
	TupleHeaderSize uint16 = 12
)

// === SlottedPage Define ===
//
// SlottedPage is a slotted-page view over a raw Page frame: it holds a pointer
// to the frame's backing array and its accessors (Header/SlotEntryAt/Tuple) read
// and write through that array in place. It copies nothing, so it is valid only
// while the underlying frame is pinned. Build one with AsSlottedPage.
//
// Being a pointer view, copying a SlottedPage by value is harmless — the copy
// aliases the same frame — so it needs no noCopy guard.
type SlottedPage struct {
	data *[PageSize]byte
}

// AsSlottedPage overlays the slotted-page accessors on a raw frame without
// copying: the returned view aliases p's bytes. A brand-new frame must be
// formatted with Init before tuples can be inserted or counted.
func AsSlottedPage(p *Page) *SlottedPage {
	return &SlottedPage{&p.data}
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

// Bytes exposes the underlying frame's raw storage as a view (not a copy): the
// slice aliases the frame's backing array, so writing through it mutates the
// page and bypasses every accessor in this package. It is the same storage as
// the source Page's Bytes; use it for I/O, not as a general escape hatch.
func (p *SlottedPage) Bytes() []byte {
	return p.data[:]
}

// Init formats the page as a valid, ready-to-use empty page: it zeroes the
// backing array — so no bytes from a frame's previous occupant survive — and
// sets the boundary pointers that make the free-space and SlotCount arithmetic
// well-defined. A merely zeroed page is NOT valid: with pd_upper == 0,
// SlotCount() computes (0 - HeaderSize) and underflows to a garbage count.
//
// After Init the slot directory is empty and the whole span between the header
// and the end of the page is free space.
func (p *SlottedPage) Init() {
	clear(p.data[:])
	h := p.Header()
	h.SetPdUpper(HeaderSize) // empty directory: it ends where it begins
	h.SetPdLower(PageSize)   // no tuples yet: free space runs to the page end
	h.SetPdPagesize(PageSize)
}

// FreeSpace reports the bytes available between the slot directory and the tuple
// data area. A new tuple needs its own length plus one SlotEntrySize-byte slot,
// so it fits only when FreeSpace() >= len(tuple) + SlotEntrySize.
func (p *SlottedPage) FreeSpace() uint16 {
	h := p.Header()
	return h.PdLower() - h.PdUpper()
}

// InsertTuple copies a fully-assembled tuple's bytes into the page and appends a
// slot pointing at them, returning the new slot's index. The bytes are opaque to
// the page: assembling the tuple (its header, MVCC fields, columns) is the
// caller's job — the page only manages placement.
//
// A tuple consumes its own length plus one SlotEntrySize slot. If the free space
// cannot hold both, it returns ErrNoSpace and leaves the page unchanged, so the
// caller can try another page. v1 only ever appends into the free space; it does
// not reclaim the space of deleted tuples (compaction/vacuum, deferred).
func (p *SlottedPage) InsertTuple(data []byte) (uint16, error) {
	// Compare in int to avoid a uint16 overflow when data is absurdly large.
	if len(data)+int(SlotEntrySize) > int(p.FreeSpace()) {
		return 0, ErrNoSpace
	}
	dataLen := uint16(len(data))

	h := p.Header()
	slotIndex := p.SlotCount()
	tupleOffset := h.PdLower() - dataLen

	// Write into the free space first, then advance the boundary pointers to
	// commit — mirroring AllocatePage's "write, then bump the counter". The
	// setters cannot fail here (values fit their fields, guaranteed by the space
	// check), but propagating their errors keeps the page untouched if they did.
	entry := p.slotEntryAt(slotIndex) // view over the to-be slot at pd_upper
	if _, err := entry.SetOffset(tupleOffset); err != nil {
		return 0, fmt.Errorf("page: insert tuple: %w", err)
	}
	if _, err := entry.SetLength(dataLen); err != nil {
		return 0, fmt.Errorf("page: insert tuple: %w", err)
	}
	copy(p.data[tupleOffset:h.PdLower()], data)

	h.SetPdUpper(h.PdUpper() + SlotEntrySize)
	h.SetPdLower(tupleOffset)
	return slotIndex, nil
}
