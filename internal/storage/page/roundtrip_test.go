// Package page_test holds the black-box tests: they may use only the exported
// API, so they verify the contract callers actually depend on rather than the
// internal byte layout (covered by the in-package tests).
package page_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// Everything observable through the exported API, gathered so an original and
// a rebuilt page can be compared in one shot.
type pageSnapshot struct {
	lsn      uint64
	checksum uint16
	flags    uint16
	lower    uint16
	upper    uint16
	special  uint16
	pagesize uint16
	pruneXid uint32

	slots []slotSnapshot
	tuple tupleSnapshot
}

type slotSnapshot struct {
	offset uint16
	length uint16
	flag   byte
}

type tupleSnapshot struct {
	txMin    uint32
	txMax    uint32
	flags    uint8
	colCount uint16
	hoff     uint8
	hasNull  bool
}

func snapshot(p *page.SlottedPage) pageSnapshot {
	h := p.Header()
	s := pageSnapshot{
		lsn:      h.PdLsn(),
		checksum: h.PdChecksum(),
		flags:    h.PdFlags(),
		lower:    h.PdLower(),
		upper:    h.PdUpper(),
		special:  h.PdSpecial(),
		pagesize: h.PdPagesize(),
		pruneXid: h.PdPruneXid(),
	}

	for _, e := range p.Slots() {
		s.slots = append(s.slots, slotSnapshot{e.Offset(), e.Length(), e.Flag()})
	}

	if p.SlotCount() > 0 {
		first := p.SlotEntryAt(0)
		th := p.LocateTupleByEntry(&first).TupleHeader()
		s.tuple = tupleSnapshot{
			txMin:    th.TxMin(),
			txMax:    th.TxMax(),
			flags:    th.Flags(),
			colCount: th.ColumnCount(),
			hoff:     th.Hoff(),
			hasNull:  th.HasNull(),
		}
	}
	return s
}

// Assemble a page through the exported API, take its bytes out, rebuild a page
// from them, and require that both the bytes and everything readable through
// the API are identical. This is the in-memory stand-in for the disk write/read
// cycle the storage layer will perform once the I/O layer exists.
func TestPageRoundTripThroughBytes(t *testing.T) {
	// A closure so a setter's (bool, error) pair can be forwarded directly:
	// Go only allows a multi-value call as the sole argument.
	mustOK := func(ok bool, err error) {
		t.Helper()
		if err != nil || !ok {
			t.Fatalf("setter failed: ok=%v err=%v", ok, err)
		}
	}

	pg := page.NewSlottedPage([page.PageSize]byte{})

	h := pg.Header()
	h.SetPdLsn(0x0102030405060708)
	h.SetPdChecksum(0xABCD)
	h.SetPdFlags(0x0F0F)
	h.SetPdSpecial(0x1234)
	h.SetPdPagesize(page.PageSize)
	h.SetPdPruneXid(0xDEADBEEF)

	// three slots, tuple area starting at 7000
	const slots = 3
	h.SetPdUpper(page.HeaderSize + slots*page.SlotEntrySize)
	h.SetPdLower(7000)

	offsets := [slots]uint16{7000, 7100, 7200}
	for i := uint16(0); i < slots; i++ {
		e := pg.SlotEntryAt(i)
		mustOK(e.SetOffset(offsets[i]))
		mustOK(e.SetLength(page.TupleHeaderSize))
		mustOK(e.SetFlag(1)) // NORMAL
	}

	first := pg.SlotEntryAt(0)
	th := pg.LocateTupleByEntry(&first).TupleHeader()
	th.SetTxMin(1001)
	th.SetTxMax(0)
	mustOK(th.SetFlags(page.FlagHasNull | page.FlagXminCommitted))
	mustOK(th.SetColumnCount(7))
	th.SetHoff(13)

	// --- out and back in ---
	rebuilt := page.NewSlottedPage([page.PageSize]byte(pg.Bytes()))

	if !bytes.Equal(pg.Bytes(), rebuilt.Bytes()) {
		t.Fatal("rebuilt page is not byte-identical to the original")
	}
	if got, want := snapshot(rebuilt), snapshot(pg); !reflect.DeepEqual(got, want) {
		t.Errorf("state read back differs\n got: %+v\nwant: %+v", got, want)
	}
}

// A page rebuilt from bytes must be independent of the original: NewSlottedPage
// copies, so mutating one must not disturb the other.
func TestRebuiltPageIsIndependent(t *testing.T) {
	pg := page.NewSlottedPage([page.PageSize]byte{})
	pg.Header().SetPdSpecial(0x1111)

	rebuilt := page.NewSlottedPage([page.PageSize]byte(pg.Bytes()))
	rebuilt.Header().SetPdSpecial(0x2222)

	if got := pg.Header().PdSpecial(); got != 0x1111 {
		t.Errorf("original followed the rebuilt page's write: %#x, want 0x1111", got)
	}
	if got := rebuilt.Header().PdSpecial(); got != 0x2222 {
		t.Errorf("rebuilt page did not keep its own write: %#x, want 0x2222", got)
	}
}
