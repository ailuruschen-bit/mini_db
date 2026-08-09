// Package page defines the on-disk page abstractions. Page is the raw,
// layout-agnostic frame — a fixed-size block of bytes that any file kind uses to
// move data between disk and memory. Concrete layouts (SlottedPage here, a
// B+Tree node in the index layer) are views overlaid on a Page; they interpret
// its bytes without owning them.
package page

// PageSize is the fixed size of a page, in bytes. It is the unit of transfer
// between disk and memory and the size of a buffer-pool frame.
const PageSize uint16 = 8192

// noCopy triggers go vet's copylock check when a value embedding it is copied
// by value. It implements sync.Locker but does nothing at runtime and is
// zero-sized, so it adds neither behavior nor memory overhead.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// Page is a raw page frame: the top-level unit the disk and buffer layers move
// and cache, independent of what the bytes mean. It carries no interpretation —
// an access method overlays a view (AsSlottedPage here, a B+Tree node in the
// index layer, ...) to read and write structured content. Keeping the frame
// layout-agnostic is what lets one buffer pool back files of different page
// kinds.
//
// Page owns its backing array inline, so copying a Page by value would detach
// every view taken from the original; the embedded noCopy makes `go vet` flag
// any accidental value copy. Always pass it by pointer.
type Page struct {
	_    noCopy
	data [PageSize]byte
}

// NewPage returns a fresh, zeroed page frame. It is not formatted into any page
// kind: the access method that takes it (via AsSlottedPage, Init, ...) is
// responsible for formatting it before use.
func NewPage() *Page {
	return &Page{}
}

// Bytes exposes the frame's raw storage so it can be handed to the disk layer
// (disk.ReadPage/WritePage into p.Bytes()). It returns a view, not a copy: the
// slice aliases the frame's backing array. Use it for I/O.
func (p *Page) Bytes() []byte {
	return p.data[:]
}
