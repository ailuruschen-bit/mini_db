package page

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// initPage returns a freshly formatted, ready-to-insert empty page.
func initPage() *SlottedPage {
	p := blankPage()
	p.Init()
	return p
}

// readTuple reads the tuple bytes a slot points at, via the public Tuple.Bytes.
func readTuple(t *testing.T, p *SlottedPage, slot uint16) []byte {
	t.Helper()
	e := p.SlotEntryAt(slot)
	return p.LocateTupleByEntry(&e).Bytes()
}

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

// Init must turn any frame — even one still holding a previous page's bytes —
// into a valid empty page: boundary pointers set so free-space and SlotCount
// arithmetic is well-defined, and no stale bytes left behind. A merely zeroed
// page would underflow SlotCount, so this pins down the actual pointer values.
func TestSlottedPageInit(t *testing.T) {
	var raw [PageSize]byte
	for i := range raw {
		raw[i] = 0xFF // simulate leftover bytes from the frame's previous page
	}
	p := NewSlottedPage(raw)

	p.Init()

	h := p.Header()
	if got := h.PdUpper(); got != HeaderSize {
		t.Errorf("PdUpper = %d, want %d (empty directory ends at the header)", got, HeaderSize)
	}
	if got := h.PdLower(); got != PageSize {
		t.Errorf("PdLower = %d, want %d (no tuples: free space runs to the end)", got, PageSize)
	}
	if got := h.PdPagesize(); got != PageSize {
		t.Errorf("PdPagesize = %d, want %d", got, PageSize)
	}
	if got := p.SlotCount(); got != 0 {
		t.Errorf("SlotCount = %d, want 0 (an empty page has no slots)", got)
	}

	// Every byte past the header must be zero: no leftover 0xFF survived.
	for i := int(HeaderSize); i < int(PageSize); i++ {
		if p.data[i] != 0 {
			t.Fatalf("byte %d = %#x after Init, want 0 (stale data survived the format)", i, p.data[i])
		}
	}
}

// An empty page's free space is the whole page minus the header.
func TestFreeSpaceEmptyPage(t *testing.T) {
	p := initPage()
	if got, want := p.FreeSpace(), PageSize-HeaderSize; got != want {
		t.Errorf("FreeSpace on empty page = %d, want %d", got, want)
	}
}

// A single insert lands the tuple at the top of the free space, appends slot 0,
// and advances both boundary pointers. Values are pinned down explicitly.
func TestInsertTuplePlacement(t *testing.T) {
	p := initPage()
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01}

	slot, err := p.InsertTuple(data)
	if err != nil {
		t.Fatal(err)
	}
	if slot != 0 {
		t.Errorf("first slot index = %d, want 0", slot)
	}

	wantOffset := PageSize - uint16(len(data)) // tuple sits against the page end
	e := p.SlotEntryAt(0)
	if got := e.Offset(); got != wantOffset {
		t.Errorf("slot offset = %d, want %d", got, wantOffset)
	}
	if got := e.Length(); got != uint16(len(data)) {
		t.Errorf("slot length = %d, want %d", got, len(data))
	}
	if got := p.Header().PdUpper(); got != HeaderSize+SlotEntrySize {
		t.Errorf("pd_upper = %d, want %d (grew by one slot)", got, HeaderSize+SlotEntrySize)
	}
	if got := p.Header().PdLower(); got != wantOffset {
		t.Errorf("pd_lower = %d, want %d (grew down by the tuple length)", got, wantOffset)
	}
	if got := p.data[wantOffset : wantOffset+uint16(len(data))]; !bytes.Equal(got, data) {
		t.Errorf("tuple bytes = %x, want %x", got, data)
	}
	if got := p.SlotCount(); got != 1 {
		t.Errorf("SlotCount = %d, want 1", got)
	}
}

// Several inserts each get consecutive slots and read back intact — proving they
// do not overlap.
func TestInsertTupleMultipleRoundTrip(t *testing.T) {
	p := initPage()
	tuples := [][]byte{
		{0x11, 0x22},
		{0x33, 0x44, 0x55},
		{0x66},
	}
	for i, data := range tuples {
		slot, err := p.InsertTuple(data)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if slot != uint16(i) {
			t.Errorf("insert %d: slot = %d, want %d", i, slot, i)
		}
	}
	if got := p.SlotCount(); got != uint16(len(tuples)) {
		t.Fatalf("SlotCount = %d, want %d", got, len(tuples))
	}
	for i, data := range tuples {
		if got := readTuple(t, p, uint16(i)); !bytes.Equal(got, data) {
			t.Errorf("tuple %d = %x, want %x", i, got, data)
		}
	}
}

// A tuple that does not fit is rejected with ErrNoSpace and must not disturb the
// page.
func TestInsertTupleNoSpaceLeavesPageUnchanged(t *testing.T) {
	p := initPage()

	// Fill the page down to 10 free bytes.
	big := make([]byte, int(PageSize-HeaderSize-SlotEntrySize)-10)
	for i := range big {
		big[i] = 0xAB
	}
	if _, err := p.InsertTuple(big); err != nil {
		t.Fatalf("setup insert: %v", err)
	}

	upper, lower, count := p.Header().PdUpper(), p.Header().PdLower(), p.SlotCount()

	if _, err := p.InsertTuple(make([]byte, 20)); !errors.Is(err, ErrNoSpace) { // needs 24 > 10
		t.Errorf("err = %v, want ErrNoSpace", err)
	}
	if p.Header().PdUpper() != upper || p.Header().PdLower() != lower || p.SlotCount() != count {
		t.Error("a failed insert must leave the page unchanged")
	}
}

// Data larger than a page (and larger than a uint16) is reported as ErrNoSpace,
// not silently truncated by an overflow.
func TestInsertTupleTooLargeReturnsNoSpace(t *testing.T) {
	p := initPage()
	if _, err := p.InsertTuple(make([]byte, 70000)); !errors.Is(err, ErrNoSpace) {
		t.Errorf("err = %v, want ErrNoSpace", err)
	}
	if got := p.SlotCount(); got != 0 {
		t.Errorf("SlotCount = %d, want 0 (nothing inserted)", got)
	}
}

// Robustness: if the page's pointers were corrupt such that a slot's offset
// would overflow its 15-bit field, InsertTuple fails rather than writing a
// truncated offset that would point somewhere wrong.
func TestInsertTupleRejectsOverflowingOffset(t *testing.T) {
	p := initPage()
	p.Header().SetPdLower(40000) // corrupt: beyond a real page; offset would exceed 32767

	_, err := p.InsertTuple([]byte{0x01})
	if err == nil || errors.Is(err, ErrNoSpace) {
		t.Errorf("err = %v, want a field-overflow error", err)
	}
	if got := p.SlotCount(); got != 0 {
		t.Errorf("SlotCount = %d, want 0 (nothing committed)", got)
	}
}

// Likewise a length that would overflow the slot's 15-bit length field is
// rejected, and the boundary pointers are left uncommitted.
func TestInsertTupleRejectsOverflowingLength(t *testing.T) {
	p := initPage()
	p.Header().SetPdLower(60000) // corrupt: lots of apparent free space

	// offset (60000 - len) stays in range, but the length itself overflows.
	_, err := p.InsertTuple(make([]byte, 40000))
	if err == nil || errors.Is(err, ErrNoSpace) {
		t.Errorf("err = %v, want a field-overflow error", err)
	}
	if got := p.SlotCount(); got != 0 {
		t.Errorf("SlotCount = %d, want 0 (nothing committed)", got)
	}
}
