package storage

import (
	"encoding/binary"
)

// === pageHeader Define ===
type PageHeader struct {
	data *[HeaderSize]byte
}

// --- Core Function ---

// --- All Header Field (Value Getter + Setter)---

// pd_lsn: point to the WAL log of the last page upgrade
func (h *PageHeader) PdLsn() uint64 {
	return binary.BigEndian.Uint64(h.data[0:8])
}

func (h *PageHeader) SetPdLsn(pd uint64) {
	binary.BigEndian.PutUint64(h.data[0:8], pd)
}

// pd_checksum: use to check the consistency of data
func (h *PageHeader) PdChecksum() uint16 {
	return binary.BigEndian.Uint16(h.data[8:10])
}

func (h *PageHeader) SetPdChecksum(pd uint16) {
	binary.BigEndian.PutUint16(h.data[8:10], pd)
}

// pd_flags: show page status
func (h *PageHeader) PdFlags() uint16 {
	return binary.BigEndian.Uint16(h.data[10:12])
}

func (h *PageHeader) SetPdFlags(pd uint16) {
	binary.BigEndian.PutUint16(h.data[10:12], pd)
}

/* pd_lower and pd_upper always point into the Free Space */
// pd_lower: point to the bottom of free space ( ~ 8191)
func (h *PageHeader) PdLower() uint16 {
	return binary.BigEndian.Uint16(h.data[12:14])
}

func (h *PageHeader) SetPdLower(pd uint16) {
	binary.BigEndian.PutUint16(h.data[12:14], pd)
}

// pd_upper: point to the top of free space (24 ~ )
func (h *PageHeader) PdUpper() uint16 {
	return binary.BigEndian.Uint16(h.data[14:16])
}

func (h *PageHeader) SetPdUpper(pd uint16) {
	binary.BigEndian.PutUint16(h.data[14:16], pd)
}

// pd_special: used for index
func (h *PageHeader) PdSpecial() uint16 {
	return binary.BigEndian.Uint16(h.data[16:18])
}

func (h *PageHeader) SetPdSpecial(pd uint16) {
	binary.BigEndian.PutUint16(h.data[16:18], pd)
}

// pd_pagesize: page size and version
func (h *PageHeader) PdPagesize() uint16 {
	return binary.BigEndian.Uint16(h.data[18:20])
}

func (h *PageHeader) SetPdPagesize(pd uint16) {
	binary.BigEndian.PutUint16(h.data[18:20], pd)
}

// pd_prune_xid: used for the cleanup of unused tuple
func (h *PageHeader) PdPruneXid() uint32 {
	return binary.BigEndian.Uint32(h.data[20:24])
}

func (h *PageHeader) SetPdPruneXid(pd uint32) {
	binary.BigEndian.PutUint32(h.data[20:24], pd)
}

// --- Tool Functions ---
