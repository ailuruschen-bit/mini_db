package page

import (
	"bytes"
	"testing"
)

// entryField describes one bit-packed field of the 4-byte slot entry.
// The 32-bit word is laid out as offset(15) | length(15) | flags(2); a field's
// width is all the descriptor needs, since each field's bit position is pinned
// by TestSlotEntryGoldenLayout rather than recomputed here.
type entryField struct {
	name  string
	width uint
	set   func(s *SlotEntry, v uint32) (bool, error)
	get   func(s *SlotEntry) uint32
}

func (f entryField) max() uint32 { return (1 << f.width) - 1 }

func entryFields() []entryField {
	return []entryField{
		{"offset", 15,
			func(s *SlotEntry, v uint32) (bool, error) { return s.SetOffset(uint16(v)) },
			func(s *SlotEntry) uint32 { return uint32(s.Offset()) }},
		{"length", 15,
			func(s *SlotEntry, v uint32) (bool, error) { return s.SetLength(uint16(v)) },
			func(s *SlotEntry) uint32 { return uint32(s.Length()) }},
		{"flag", 2,
			func(s *SlotEntry, v uint32) (bool, error) { return s.SetFlag(byte(v)) },
			func(s *SlotEntry) uint32 { return uint32(s.Flag()) }},
	}
}

// mustSet fails the test if a setter reports failure. t.Helper() makes a
// failure point at the calling line rather than at this function.
func mustSet(t *testing.T, name string, ok bool, err error) {
	t.Helper()
	if err != nil || !ok {
		t.Fatalf("%s: unexpected setter failure (ok=%v, err=%v)", name, ok, err)
	}
}

// Round-trip + boundaries: 0, 1 and the field maximum survive set/get.
func TestSlotEntryRoundTrip(t *testing.T) {
	for _, f := range entryFields() {
		t.Run(f.name, func(t *testing.T) {
			s := blankSlotEntry()
			for _, v := range []uint32{0, 1, f.max()} {
				ok, err := f.set(s, v)
				mustSet(t, f.name, ok, err)
				if got := f.get(s); got != v {
					t.Errorf("set %d, got %d", v, got)
				}
			}
		})
	}
}

// Bit-field independence: the three fields share one 32-bit word, so a bad
// mask in any setter corrupts its neighbours. Run from both a zero and an
// all-max background: the max background is what proves a setter actually
// clears its bits instead of only OR-ing the new value in.
func TestSlotEntryFieldIndependence(t *testing.T) {
	fields := entryFields()

	directions := []struct {
		name string
		bg   func(f entryField) uint32
		poke func(f entryField) uint32
	}{
		{"zero-background",
			func(entryField) uint32 { return 0 },
			func(f entryField) uint32 { return f.max() }},
		{"max-background",
			func(f entryField) uint32 { return f.max() },
			func(entryField) uint32 { return 0 }},
	}

	for _, d := range directions {
		for _, target := range fields {
			t.Run(d.name+"/"+target.name, func(t *testing.T) {
				s := blankSlotEntry()
				for _, f := range fields {
					ok, err := f.set(s, d.bg(f))
					mustSet(t, f.name, ok, err)
				}
				ok, err := target.set(s, d.poke(target))
				mustSet(t, target.name, ok, err)

				for _, f := range fields {
					want := d.bg(f)
					if f.name == target.name {
						want = d.poke(f)
					}
					if got := f.get(s); got != want {
						t.Errorf("after writing %s: field %s = %d, want %d", target.name, f.name, got, want)
					}
				}
			})
		}
	}
}

// Error path: a value one past the field maximum must be rejected and must
// leave the stored value untouched.
func TestSlotEntryRejectsOverflow(t *testing.T) {
	for _, f := range entryFields() {
		t.Run(f.name, func(t *testing.T) {
			s := blankSlotEntry()

			// seed a known-good value so we can prove the failed write is a no-op
			const seed = 1
			ok, err := f.set(s, seed)
			mustSet(t, f.name, ok, err)

			ok, err = f.set(s, f.max()+1)
			if ok || err == nil {
				t.Errorf("set(%d) succeeded (ok=%v, err=%v), want rejection", f.max()+1, ok, err)
			}
			if got := f.get(s); got != seed {
				t.Errorf("rejected write modified the field: got %d, want %d", got, seed)
			}
		})
	}
}

// Golden layout: a known triple must produce exactly these 4 bytes.
//
// The values are layout probes: non-zero (so "never written" is distinguishable
// from "written correctly"), distinct, and asymmetric (so a reversed byte order
// shows up). flag is 0b10 rather than 0 or 0b11 specifically so that its two
// bits differ — with an all-zero or all-one flag, swapping the two bits would
// be invisible. Expected bytes are hard-coded, never recomputed from the same
// shifts the implementation uses.
//
//	offset = 0x1234 -> << 17 = 0x2468_0000
//	length = 0x0ABC -> <<  2 = 0x0000_2AF0
//	flag   = 0x2    -> <<  0 = 0x0000_0002
//	                    word = 0x2468_2AF2
func TestSlotEntryGoldenLayout(t *testing.T) {
	s := blankSlotEntry()

	ok, err := s.SetOffset(0x1234)
	mustSet(t, "offset", ok, err)
	ok, err = s.SetLength(0x0ABC)
	mustSet(t, "length", ok, err)
	ok, err = s.SetFlag(0x2)
	mustSet(t, "flag", ok, err)

	want := []byte{0x24, 0x68, 0x2A, 0xF2}
	if got := s.data[:]; !bytes.Equal(got, want) {
		t.Errorf("raw bytes = % x, want % x", got, want)
	}
}
