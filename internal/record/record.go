// Package record encodes typed rows to and from tuple bytes — the opaque
// []byte the heap stores. A Schema describes a table's columns; Encode
// serializes a Row into a tuple (TupleHeader + optional null bitmap + column
// data), and Decode reverses it. The 12-byte tuple header is written and read
// through the page package, the authority on that layout; the null bitmap and
// column encoding are this package's own.
package record

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// Type is a column's data type.
type Type uint8

const (
	TypeInt  Type = iota // int64, 8 bytes big-endian
	TypeText             // string, uint16 length prefix + UTF-8 bytes
)

// Column describes one column: its name, type, and whether it may be NULL.
type Column struct {
	Name     string
	Type     Type
	Nullable bool
}

// Schema is a table's column layout; it encodes and decodes its own rows.
type Schema struct {
	Columns []Column
}

// Row holds one row's values, one per column in schema order. A value is an
// int64 (TypeInt), a string (TypeText), or nil (NULL).
type Row []any

// Encode serializes row into tuple bytes: a TupleHeader, an optional null
// bitmap (present only when some column is NULL), then the non-null column data
// in schema order. MVCC fields (t_xmin/t_xmax) are left zero for the future
// transaction layer to fill.
func (s Schema) Encode(row Row) ([]byte, error) {
	if len(row) != len(s.Columns) {
		return nil, fmt.Errorf("record: row has %d values, schema has %d columns", len(row), len(s.Columns))
	}

	// Validate every value and size the column-data region.
	hasNull := false
	dataLen := 0
	for i, col := range s.Columns {
		v := row[i]
		if v == nil {
			if !col.Nullable {
				return nil, fmt.Errorf("record: column %d (%q) is not nullable but value is nil", i, col.Name)
			}
			hasNull = true
			continue
		}
		switch col.Type {
		case TypeInt:
			if _, ok := v.(int64); !ok {
				return nil, fmt.Errorf("record: column %d (%q) expects int64, got %T", i, col.Name, v)
			}
			dataLen += 8
		case TypeText:
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("record: column %d (%q) expects string, got %T", i, col.Name, v)
			}
			if len(str) > math.MaxUint16 {
				return nil, fmt.Errorf("record: column %d (%q) text is %d bytes, max %d", i, col.Name, len(str), math.MaxUint16)
			}
			dataLen += 2 + len(str)
		default:
			return nil, fmt.Errorf("record: column %d (%q) has unknown type %d", i, col.Name, col.Type)
		}
	}

	bitmapSize := 0
	if hasNull {
		bitmapSize = (len(s.Columns) + 7) / 8
	}
	hoff := int(page.TupleHeaderSize) + bitmapSize
	buf := make([]byte, hoff+dataLen)

	// Header. SetColumnCount validates the 10-bit field (rejects > 1023 columns);
	// SetFlags cannot fail here because FlagHasNull is a valid 6-bit value.
	h := page.NewTuple(buf).TupleHeader()
	if _, err := h.SetColumnCount(uint16(len(s.Columns))); err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}
	h.SetHoff(uint8(hoff))
	if hasNull {
		_, _ = h.SetFlags(page.FlagHasNull)
	}

	// Null bitmap: bit i set means column i is NULL.
	bitmap := buf[page.TupleHeaderSize:hoff]
	for i := range s.Columns {
		if row[i] == nil {
			bitmap[i/8] |= 1 << (uint(i) % 8)
		}
	}

	// Column data, non-null columns only.
	pos := hoff
	for i, col := range s.Columns {
		v := row[i]
		if v == nil {
			continue
		}
		switch col.Type {
		case TypeInt:
			binary.BigEndian.PutUint64(buf[pos:], uint64(v.(int64)))
			pos += 8
		case TypeText:
			str := v.(string)
			binary.BigEndian.PutUint16(buf[pos:], uint16(len(str)))
			pos += 2
			pos += copy(buf[pos:], str)
		}
	}
	return buf, nil
}

// Decode parses tuple bytes back into a Row using this schema. It rejects bytes
// that do not match the schema (wrong column count) or that are truncated or
// internally inconsistent.
func (s Schema) Decode(data []byte) (Row, error) {
	th := int(page.TupleHeaderSize)
	if len(data) < th {
		return nil, fmt.Errorf("record: %d bytes is shorter than the %d-byte header", len(data), th)
	}
	h := page.NewTuple(data).TupleHeader()
	if int(h.ColumnCount()) != len(s.Columns) {
		return nil, fmt.Errorf("record: tuple has %d columns, schema has %d", h.ColumnCount(), len(s.Columns))
	}

	bitmapSize := 0
	if h.HasNull() {
		bitmapSize = (len(s.Columns) + 7) / 8
	}
	hoff := th + bitmapSize
	if int(h.Hoff()) != hoff {
		return nil, fmt.Errorf("record: header offset %d inconsistent with %d columns (hasNull=%v)", h.Hoff(), len(s.Columns), h.HasNull())
	}
	if len(data) < hoff {
		return nil, fmt.Errorf("record: %d bytes truncated before column data (need %d)", len(data), hoff)
	}
	bitmap := data[th:hoff] // empty when there are no NULLs

	row := make(Row, len(s.Columns))
	pos := hoff
	for i, col := range s.Columns {
		if h.HasNull() && bitmap[i/8]&(1<<(uint(i)%8)) != 0 {
			continue // leave row[i] == nil
		}
		switch col.Type {
		case TypeInt:
			if pos+8 > len(data) {
				return nil, fmt.Errorf("record: truncated int at column %d", i)
			}
			row[i] = int64(binary.BigEndian.Uint64(data[pos:]))
			pos += 8
		case TypeText:
			if pos+2 > len(data) {
				return nil, fmt.Errorf("record: truncated text length at column %d", i)
			}
			n := int(binary.BigEndian.Uint16(data[pos:]))
			pos += 2
			if pos+n > len(data) {
				return nil, fmt.Errorf("record: truncated text at column %d", i)
			}
			row[i] = string(data[pos : pos+n])
			pos += n
		default:
			return nil, fmt.Errorf("record: column %d has unknown type %d", i, col.Type)
		}
	}
	return row, nil
}
