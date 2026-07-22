package storage

import (
	"encoding/binary"
	"testing"
)

// NewSlottedPage takes its argument by value, so the page must own a copy of
// the bytes. A caller mutating its own array afterwards must not be able to
// reach into the page.
func TestNewSlottedPageCopiesInput(t *testing.T) {
	var raw [PageSize]byte
	raw[0] = 0xAA
	raw[PageSize-1] = 0xAA

	p := NewSlottedPage(raw)

	raw[0] = 0xBB
	raw[PageSize-1] = 0xBB

	if got := p.data[0]; got != 0xAA {
		t.Errorf("first byte followed a later mutation of the caller's array: %#x, want 0xAA", got)
	}
	if got := p.data[PageSize-1]; got != 0xAA {
		t.Errorf("last byte followed a later mutation of the caller's array: %#x, want 0xAA", got)
	}
}

// Header() must hand out a view over the page's backing array, not a copy:
// a write through one view is visible in the raw bytes and through any view
// obtained later.
func TestHeaderAliasesBackingArray(t *testing.T) {
	p := blankPage()

	p.Header().SetPdUpper(0x1234)

	if got := binary.BigEndian.Uint16(p.data[14:16]); got != 0x1234 {
		t.Errorf("write through Header() did not reach the page: raw = %#x", got)
	}
	if got := p.Header().PdUpper(); got != 0x1234 {
		t.Errorf("a freshly obtained Header() did not observe the write: %#x", got)
	}
}

// The slot directory spans [HeaderSize, pd_upper), so pd_upper alone decides
// how many entries exist. An untouched page (pd_upper == HeaderSize) has none.
func TestSlotCount(t *testing.T) {
	tests := []struct {
		name string
		n    uint16
	}{
		{"empty page", 0},
		{"single slot", 1},
		{"several slots", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := blankPage()
			p.Header().SetPdUpper(HeaderSize + tt.n*SlotEntrySize)

			if got := p.SlotCount(); got != tt.n {
				t.Errorf("got %d entries, want %d", got, tt.n)
			}
		})
	}
}

// Slot i must map to the 4-byte window at HeaderSize+i*SlotEntrySize, and
// writing through the returned entry must reach the page — the entry is a copy
// but carries a pointer into the backing array.
func TestSlotEntryAtMapsToItsSlot(t *testing.T) {
	const n = 4
	p := blankPage()
	p.Header().SetPdUpper(HeaderSize + n*SlotEntrySize)

	for i := uint16(0); i < n; i++ {
		e := p.SlotEntryAt(i)
		ok, err := e.SetOffset(1000 + i)
		mustSet(t, "offset", ok, err)
	}

	// Freshly obtained entries must observe those writes.
	for i := uint16(0); i < n; i++ {
		e := p.SlotEntryAt(i)
		if got, want := e.Offset(), 1000+i; got != want {
			t.Errorf("slot %d: offset = %d, want %d", i, got, want)
		}
	}

	// And each entry must sit at its own window in the page.
	for i := 0; i < n; i++ {
		at := int(HeaderSize) + i*int(SlotEntrySize)
		word := binary.BigEndian.Uint32(p.data[at : at+int(SlotEntrySize)])
		if got, want := uint16(word>>17), uint16(1000+i); got != want {
			t.Errorf("slot %d at byte %d: offset = %d, want %d", i, at, got, want)
		}
	}
}

// Asking for a slot the directory does not have is a programming error, not a
// silent read of free space.
func TestSlotEntryAtPanicsOutOfRange(t *testing.T) {
	p := blankPage()
	p.Header().SetPdUpper(HeaderSize + 2*SlotEntrySize)

	defer func() {
		if recover() == nil {
			t.Error("SlotEntryAt(2) on a 2-slot page did not panic")
		}
	}()
	_ = p.SlotEntryAt(2)
}

// Slots must yield every entry in order, and must support early exit.
func TestSlotsIteration(t *testing.T) {
	const n = 5
	p := blankPage()
	p.Header().SetPdUpper(HeaderSize + n*SlotEntrySize)
	for i := uint16(0); i < n; i++ {
		e := p.SlotEntryAt(i)
		ok, err := e.SetOffset(1000 + i)
		mustSet(t, "offset", ok, err)
	}

	var seen []uint16
	for i, e := range p.Slots() {
		if got, want := e.Offset(), 1000+i; got != want {
			t.Errorf("slot %d: offset = %d, want %d", i, got, want)
		}
		seen = append(seen, i)
	}
	if len(seen) != n {
		t.Errorf("iterated %d slots, want %d", len(seen), n)
	}
	for i, idx := range seen {
		if idx != uint16(i) {
			t.Errorf("slots yielded out of order: %v", seen)
			break
		}
	}

	// `break` must stop the iteration.
	count := 0
	for range p.Slots() {
		count++
		break
	}
	if count != 1 {
		t.Errorf("break did not stop iteration: ran %d times", count)
	}
}

// Indexed access and iteration are the allocation-free path; that property is
// the whole reason they exist, so lock it in.
//
// The entries are read through pointer-receiver methods, so each call takes the
// address of a local copy. That must stay on the stack — if the entry ever
// escapes, iteration silently becomes one heap allocation per slot, which is
// exactly the regression this test exists to catch.
func TestSlotAccessDoesNotAllocate(t *testing.T) {
	p := blankPage()
	p.Header().SetPdUpper(HeaderSize + 100*SlotEntrySize)

	if got := testing.AllocsPerRun(100, func() {
		e := p.SlotEntryAt(50)
		_ = e.Offset()
	}); got != 0 {
		t.Errorf("SlotEntryAt allocated %v times per run, want 0", got)
	}

	if got := testing.AllocsPerRun(100, func() {
		for _, e := range p.Slots() {
			_ = e.Offset()
		}
	}); got != 0 {
		t.Errorf("Slots allocated %v times per run, want 0", got)
	}
}

// LocateTupleByEntry must return a view covering exactly [offset, offset+length)
// — no neighbouring byte included, and writes through it reach the page.
func TestLocateTupleByEntry(t *testing.T) {
	const off, length = 4000, 24

	p := blankPage()
	e := blankSlotEntry()
	ok, err := e.SetOffset(off)
	mustSet(t, "offset", ok, err)
	ok, err = e.SetLength(length)
	mustSet(t, "length", ok, err)

	// paint the region and both neighbouring bytes
	p.data[off-1] = 0xEE
	p.data[off] = 0xAA
	p.data[off+length-1] = 0xBB
	p.data[off+length] = 0xEE

	tup := p.LocateTupleByEntry(e)

	if got := len(tup.data); got != length {
		t.Fatalf("tuple length = %d, want %d", got, length)
	}
	if got := tup.data[0]; got != 0xAA {
		t.Errorf("tuple starts at the wrong byte: %#x, want 0xAA", got)
	}
	if got := tup.data[length-1]; got != 0xBB {
		t.Errorf("tuple ends at the wrong byte: %#x, want 0xBB", got)
	}

	// writing through the tuple must reach the page, and must not spill past it
	tup.data[0] = 0x11
	tup.data[length-1] = 0x22
	if p.data[off] != 0x11 || p.data[off+length-1] != 0x22 {
		t.Error("write through the tuple did not reach the page")
	}
	if p.data[off-1] != 0xEE || p.data[off+length] != 0xEE {
		t.Error("write through the tuple spilled onto a neighbouring byte")
	}
}
