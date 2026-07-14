package storage

const (
	PageSize        uint16 = 8192
	HeaderSize      uint16 = 24
	SlotEntrySize   uint16 = 4
	TupleHeaderSize uint16 = 8
)

// === SlottedPage Define ===
type SlottedPage struct {
	data [PageSize]byte
}

// --- Core Function ---

// --- All Page Filed (Object Getter + Setter) ---

// Page Header: Metadata of Page
func (p *SlottedPage) Header() *PageHeader {
	return &PageHeader{(*[HeaderSize]byte)(p.data[:HeaderSize])}
}

// Slot Directory: an array of SlotEntry
func (p *SlottedPage) SlotDirectory() []SlotEntry {
	h := p.Header()

	// make capacity for pointer array
	count := (h.PdUpper() - HeaderSize) / SlotEntrySize
	entries := make([]SlotEntry, 0, count)

	// make slices from slotEntry area
	slotEntryArea := p.data[HeaderSize:h.PdUpper()]
	for i := uint16(0); i < count; i++ {
		entryData := slotEntryArea[i*SlotEntrySize : (i+1)*SlotEntrySize]
		entries = append(entries, SlotEntry{(*[SlotEntrySize]byte)(entryData)})
	}

	return entries
}

// Find Tuple by the pointer val (from an Entry)
func (p *SlottedPage) LocateTupleByEntry(entry *SlotEntry) *Tuple {
	return &Tuple{p.data[entry.Offset() : entry.Offset()+entry.Length()]}
}

// --- Tool Function ---
func NewSlottedPage(data [PageSize]byte) *SlottedPage {
	return &SlottedPage{data}
}
