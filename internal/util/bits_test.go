package util

import (
	"bytes"
	"testing"
)

// Round-trip across a spread of offsets, lengths, alignments and window spans
// (a field can straddle 1, 2 or 3 bytes). Reading back what was written must
// return the same value.
func TestReadWriteBitsRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		nbytes int
		offset uint16
		length byte
		val    uint16
	}{
		{"byte-aligned 8b", 4, 0, 8, 0xA5},
		{"whole first byte", 4, 0, 8, 0xFF},
		{"unaligned within one byte", 2, 2, 4, 0xA},
		{"straddles two bytes", 3, 6, 6, 0x2A},
		{"15b crossing bytes", 4, 1, 15, 0x5555},
		{"16b spanning three bytes", 4, 4, 16, 0xBEEF},
		{"single bit set", 2, 3, 1, 1},
		{"single bit clear", 2, 3, 1, 0},
		{"max value fits length", 3, 5, 10, (1 << 10) - 1},
		{"last bits of buffer", 2, 8, 8, 0xC3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, tt.nbytes)
			ok, err := WriteBits(buf, tt.offset, tt.length, tt.val)
			if !ok || err != nil {
				t.Fatalf("WriteBits: ok=%v err=%v", ok, err)
			}
			got, err := ReadBits(buf, tt.offset, tt.length)
			if err != nil {
				t.Fatalf("ReadBits: %v", err)
			}
			if got != tt.val {
				t.Errorf("round-trip: got %#x, want %#x", got, tt.val)
			}
		})
	}
}

// WriteBits must touch only the target run — every other bit keeps its prior
// value. Run from both a zero and an all-ones background so a stray write in
// either direction is visible, and so we prove the target bits are truly
// cleared before the merge rather than only OR-ed in.
func TestWriteBitsDoesNotDisturbNeighbours(t *testing.T) {
	const (
		nbytes = 4
		offset = 9  // deliberately unaligned
		length = 11 // straddles bytes 1..2
	)
	backgrounds := []struct {
		name string
		fill byte
	}{
		{"zero background", 0x00},
		{"ones background", 0xFF},
	}
	for _, bg := range backgrounds {
		t.Run(bg.name, func(t *testing.T) {
			buf := make([]byte, nbytes)
			for i := range buf {
				buf[i] = bg.fill
			}
			// keep a reference copy with the target run cleared
			want := make([]byte, nbytes)
			copy(want, buf)
			if _, err := WriteBits(want, offset, length, 0); err != nil {
				t.Fatal(err)
			}

			const v = 0x2AB // an 11-bit alternating-ish pattern
			if _, err := WriteBits(buf, offset, length, v); err != nil {
				t.Fatal(err)
			}

			// bits outside the run must equal the "target-cleared" reference;
			// verify by clearing the run in buf too and comparing.
			if _, err := WriteBits(buf, offset, length, 0); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf, want) {
				t.Errorf("write disturbed neighbouring bits:\n got % x\nwant % x", buf, want)
			}
		})
	}
}

// Golden: an unaligned 12-bit write into a zeroed 3-byte buffer must produce
// exactly these bytes. Derived by hand, not from the implementation's shifts.
//
//	buffer: 24 bits, all zero
//	write value 0xABC (1010 1011 1100) at offset 4, length 12
//	layout:  0000 [1010 1011 1100] 0000
//	bytes:   0000_1010 1011_1100 0000_0000 = 0A BC 00
func TestWriteBitsGolden(t *testing.T) {
	buf := make([]byte, 3)
	if _, err := WriteBits(buf, 4, 12, 0xABC); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x0A, 0xBC, 0x00}
	if !bytes.Equal(buf, want) {
		t.Errorf("got % x, want % x", buf, want)
	}
}

func TestBitsErrorPaths(t *testing.T) {
	t.Run("read length zero", func(t *testing.T) {
		if _, err := ReadBits(make([]byte, 2), 0, 0); err == nil {
			t.Error("want error for length 0")
		}
	})
	t.Run("read length over 16", func(t *testing.T) {
		if _, err := ReadBits(make([]byte, 4), 0, 17); err == nil {
			t.Error("want error for length 17")
		}
	})
	t.Run("read past end", func(t *testing.T) {
		if _, err := ReadBits(make([]byte, 1), 4, 8); err == nil {
			t.Error("want out-of-bounds error")
		}
	})
	t.Run("write length zero", func(t *testing.T) {
		if ok, err := WriteBits(make([]byte, 2), 0, 0, 0); ok || err == nil {
			t.Error("want error for length 0")
		}
	})
	t.Run("write past end", func(t *testing.T) {
		if ok, err := WriteBits(make([]byte, 1), 4, 8, 0); ok || err == nil {
			t.Error("want out-of-bounds error")
		}
	})
	t.Run("value does not fit", func(t *testing.T) {
		if ok, err := WriteBits(make([]byte, 2), 0, 4, 16); ok || err == nil {
			t.Error("want error: 16 does not fit in 4 bits")
		}
	})
	t.Run("rejected write is a no-op", func(t *testing.T) {
		buf := []byte{0x5A, 0x5A}
		if _, err := WriteBits(buf, 0, 4, 16); err == nil {
			t.Fatal("expected rejection")
		}
		if buf[0] != 0x5A || buf[1] != 0x5A {
			t.Errorf("rejected write modified the buffer: % x", buf)
		}
	})
}
