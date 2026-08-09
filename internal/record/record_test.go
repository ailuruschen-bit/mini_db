package record_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/record"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// sampleSchema: an int, a text, and a nullable text.
func sampleSchema() record.Schema {
	return record.Schema{Columns: []record.Column{
		{Name: "id", Type: record.TypeInt},
		{Name: "name", Type: record.TypeText},
		{Name: "note", Type: record.TypeText, Nullable: true},
	}}
}

// A row survives an encode/decode round-trip, values and types intact.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	s := sampleSchema()
	rows := []record.Row{
		{int64(42), "alice", "hello"},
		{int64(-1), "", "x"},                       // empty text is not NULL
		{int64(9223372036854775807), "max", "tail"}, // int64 boundary
	}
	for _, want := range rows {
		b, err := s.Encode(want)
		if err != nil {
			t.Fatalf("encode %v: %v", want, err)
		}
		got, err := s.Decode(b)
		if err != nil {
			t.Fatalf("decode %v: %v", want, err)
		}
		if !reflect.DeepEqual([]any(got), []any(want)) {
			t.Errorf("round-trip = %#v, want %#v", got, want)
		}
	}
}

// A NULL in a nullable column round-trips as nil, and sets the header's HasNull.
func TestNullRoundTrip(t *testing.T) {
	s := sampleSchema()
	want := record.Row{int64(1), "bob", nil}

	b, err := s.Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	if !page.NewTuple(b).TupleHeader().HasNull() {
		t.Error("HasNull flag not set for a row containing NULL")
	}
	got, err := s.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]any(got), []any(want)) {
		t.Errorf("round-trip = %#v, want %#v", got, want)
	}
}

// With no NULLs, no bitmap is written: t_hoff stays at the header size and
// HasNull is clear. Also checks the header interop with the page layer.
func TestNoNullOmitsBitmap(t *testing.T) {
	s := sampleSchema()
	b, err := s.Encode(record.Row{int64(7), "x", "y"})
	if err != nil {
		t.Fatal(err)
	}
	h := page.NewTuple(b).TupleHeader()
	if h.HasNull() {
		t.Error("HasNull set for a row with no NULLs")
	}
	if got := h.Hoff(); got != uint8(page.TupleHeaderSize) {
		t.Errorf("t_hoff = %d, want %d (no bitmap)", got, page.TupleHeaderSize)
	}
	if got := h.ColumnCount(); got != 3 {
		t.Errorf("col_count = %d, want 3", got)
	}
}

func TestEncodeErrors(t *testing.T) {
	s := sampleSchema()
	tests := []struct {
		name string
		row  record.Row
	}{
		{"too few values", record.Row{int64(1), "a"}},
		{"too many values", record.Row{int64(1), "a", "b", "c"}},
		{"nil in non-nullable", record.Row{int64(1), nil, "b"}},
		{"int column wrong type", record.Row{"not-int", "a", "b"}},
		{"text column wrong type", record.Row{int64(1), int64(2), "b"}},
		{"text too long", record.Row{int64(1), strings.Repeat("x", 1<<16), "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Encode(tt.row); err == nil {
				t.Errorf("Encode(%v) succeeded, want error", tt.row)
			}
		})
	}
}

// An unknown column type is rejected by both Encode and Decode.
func TestUnknownType(t *testing.T) {
	bad := record.Schema{Columns: []record.Column{{Name: "x", Type: record.Type(99)}}}
	if _, err := bad.Encode(record.Row{int64(1)}); err == nil {
		t.Error("Encode with an unknown type succeeded, want error")
	}

	// Decode: build a valid 1-column tuple, then decode it with the bad schema.
	good := record.Schema{Columns: []record.Column{{Name: "x", Type: record.TypeInt}}}
	b, err := good.Encode(record.Row{int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Decode(b); err == nil {
		t.Error("Decode with an unknown type succeeded, want error")
	}
}

// A schema with more columns than the 10-bit col_count field allows is rejected.
func TestTooManyColumnsRejected(t *testing.T) {
	cols := make([]record.Column, 1024) // > 1023
	row := make(record.Row, 1024)
	for i := range cols {
		cols[i] = record.Column{Name: "c", Type: record.TypeInt, Nullable: true}
		row[i] = nil
	}
	if _, err := (record.Schema{Columns: cols}).Encode(row); err == nil {
		t.Error("Encode of a 1024-column schema succeeded, want error")
	}
}

func TestDecodeErrors(t *testing.T) {
	oneInt := record.Schema{Columns: []record.Column{{Name: "n", Type: record.TypeInt}}}
	oneText := record.Schema{Columns: []record.Column{{Name: "t", Type: record.TypeText}}}

	intTuple, err := oneInt.Encode(record.Row{int64(5)}) // 12 + 8 = 20 bytes
	if err != nil {
		t.Fatal(err)
	}
	textTuple, err := oneText.Encode(record.Row{"hello"}) // 12 + 2 + 5 = 19 bytes
	if err != nil {
		t.Fatal(err)
	}

	t.Run("shorter than header", func(t *testing.T) {
		if _, err := oneInt.Decode(make([]byte, int(page.TupleHeaderSize)-1)); err == nil {
			t.Error("want error")
		}
	})
	t.Run("column count mismatch", func(t *testing.T) {
		if _, err := sampleSchema().Decode(intTuple); err == nil { // 1-col tuple, 3-col schema
			t.Error("want error")
		}
	})
	t.Run("inconsistent hoff", func(t *testing.T) {
		bad := append([]byte(nil), intTuple...)
		page.NewTuple(bad).TupleHeader().SetHoff(13) // should be 12 (no bitmap)
		if _, err := oneInt.Decode(bad); err == nil {
			t.Error("want error")
		}
	})
	t.Run("truncated before bitmap", func(t *testing.T) {
		// A null row's hoff is 13 (header + 1 bitmap byte); cut to the header.
		nullTuple, err := sampleSchema().Encode(record.Row{int64(1), "a", nil})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sampleSchema().Decode(nullTuple[:page.TupleHeaderSize]); err == nil {
			t.Error("want error")
		}
	})
	t.Run("truncated int", func(t *testing.T) {
		if _, err := oneInt.Decode(intTuple[:15]); err == nil {
			t.Error("want error")
		}
	})
	t.Run("truncated text length", func(t *testing.T) {
		if _, err := oneText.Decode(textTuple[:13]); err == nil {
			t.Error("want error")
		}
	})
	t.Run("truncated text body", func(t *testing.T) {
		if _, err := oneText.Decode(textTuple[:15]); err == nil {
			t.Error("want error")
		}
	})
}
