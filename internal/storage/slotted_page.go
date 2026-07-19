package storage

const (
	PageSize        uint16 = 8192
	HeaderSize      uint16 = 24
	SlotEntrySize   uint16 = 4
	TupleHeaderSize uint16 = 8
)

// noCopy triggers go vet's copylock check when a value embedding it is copied
// by value. It implements sync.Locker but does nothing at runtime and is
// zero-sized, so it adds neither behavior nor memory overhead.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// === SlottedPage Define ===
//
// SlottedPage owns an 8KB backing array; Header()/SlotDirectory()/Tuple all
// hand out pointers into that array. Copying a SlottedPage by value would
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

// Slot Directory: an array of SlotEntry
//
// The returned SlotEntry values are copies, but each holds a pointer into the
// page's backing array, so mutating a returned entry (via its setters) still
// writes through to this page.
func (p *SlottedPage) SlotDirectory() []SlotEntry {
	h := p.Header()

	// slot directory occupies [HeaderSize, pd_lower); pd_upper is the tuple area
	count := (h.PdLower() - HeaderSize) / SlotEntrySize
	entries := make([]SlotEntry, 0, count)

	// make slices from slotEntry area
	slotEntryArea := p.data[HeaderSize:h.PdLower()]
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
	return &SlottedPage{data: data}
}
