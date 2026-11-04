package parser

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func xrefDocument(t *testing.T, incremental bool) []byte {
	t.Helper()
	var source bytes.Buffer
	source.WriteString("%PDF-1.5\n")
	offsets := make([]int, 10)
	text := "BT /F1 12 Tf (stream page) Tj ET"
	packed := "7 0 << /Value (packed) >>"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(text), text),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Type /ObjStm /N 1 /First 4 /Length %d >>\nstream\n%s\nendstream", len(packed), packed),
	}
	for index, object := range objects {
		offsets[index+1] = source.Len()
		fmt.Fprintf(&source, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	appendXref := func(id, previous int) int {
		offset := source.Len()
		offsets[id] = offset
		entries := make([]byte, (id+1)*7)
		for object := 0; object <= id; object++ {
			entry := entries[object*7:]
			entry[0] = 1
			binary.BigEndian.PutUint32(entry[1:], uint32(offsets[object]))
			if object == 0 {
				entry[0] = 0
				binary.BigEndian.PutUint16(entry[5:], 65535)
			}
			if object == 7 {
				entry[0] = 2
				binary.BigEndian.PutUint32(entry[1:], 6)
			}
		}
		prev := ""
		if previous >= 0 {
			prev = fmt.Sprintf(" /Prev %d", previous)
		}
		fmt.Fprintf(&source, "%d 0 obj\n<< /Type /XRef /Size %d /Root 1 0 R /W [1 4 2] /Length %d%s >>\nstream\n", id, id+1, len(entries), prev)
		source.Write(entries)
		source.WriteString("\nendstream\nendobj\n")
		fmt.Fprintf(&source, "startxref\n%d\n%%%%EOF\n", offset)
		return offset
	}
	previous := appendXref(8, -1)
	if incremental {
		appendXref(9, previous)
	}
	return source.Bytes()
}

func TestXrefStreamsAndCompressedObjectsSurviveIncrementalUpdates(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		data := xrefDocument(t, incremental)
		reader, err := NewReader(t.Context(), bytes.NewReader(data), int64(len(data)), parserBudget(t).limits, nil)
		if err != nil {
			t.Fatal(err)
		}
		if reader.NumPage() != 1 {
			t.Fatalf("pages=%d", reader.NumPage())
		}
		text, err := reader.Page(1).PlainText()
		if err != nil || strings.TrimSpace(text) != "stream page" {
			t.Fatalf("text=%q error=%v", text, err)
		}
		value := reader.resolve(objptr{}, objptr{7, 0})
		if value.Key("Value").RawString() != "packed" {
			t.Fatalf("compressed object=%#v", value)
		}
		if !reader.resolve(objptr{}, objptr{100, 0}).IsNull() || !reader.resolve(objptr{}, objptr{1, 1}).IsNull() {
			t.Fatal("missing reference became another object")
		}
	}
}

func TestXrefStreamNullEntriesMaskHistoricalObjects(t *testing.T) {
	for _, entryType := range []byte{0, 3, 255} {
		t.Run(fmt.Sprint(entryType), func(t *testing.T) {
			base := xrefDocument(t, false)
			var previous int
			if _, err := fmt.Sscanf(string(base[bytes.LastIndex(base, []byte("startxref")):]), "startxref\n%d", &previous); err != nil {
				t.Fatal(err)
			}
			var source bytes.Buffer
			source.Write(base)
			offset := source.Len()
			entries := make([]byte, 21)
			entries[0], entries[7], entries[14] = entryType, entryType, 1
			binary.BigEndian.PutUint16(entries[5:], 1)
			binary.BigEndian.PutUint16(entries[12:], 1)
			binary.BigEndian.PutUint32(entries[15:], uint32(offset))
			fmt.Fprintf(&source, "9 0 obj\n<< /Type /XRef /Size 10 /Root 1 0 R /Index [4 1 7 1 9 1] /W [1 4 2] /Length %d /Prev %d >>\nstream\n", len(entries), previous)
			source.Write(entries)
			fmt.Fprintf(&source, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", offset)

			reader, err := NewReader(t.Context(), bytes.NewReader(source.Bytes()), int64(source.Len()), parserBudget(t).limits, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []uint32{4, 7} {
				if !reader.resolve(objptr{}, objptr{id, 0}).IsNull() {
					t.Fatalf("historical object %d survived a newer null entry", id)
				}
			}
			if text, err := reader.Page(1).PlainText(); err != nil || text != "" {
				t.Fatalf("deleted content=%q, error=%v", text, err)
			}
		})
	}
}

func TestXrefStreamRejectsInvalidIndexAndWidthBeforeReading(t *testing.T) {
	for _, header := range []dict{
		{"Index": array{int64(0)}},
		{},
		{"W": array{name("bad"), int64(1), int64(1)}},
		{"W": array{int64(-1), int64(1), int64(1)}},
		{"W": array{int64(9), int64(1), int64(1)}},
		{"W": array{int64(1), int64(1)}},
		{"W": array{int64(1), int64(1), int64(1)}, "Index": array{int64(-1), int64(1)}},
		{"W": array{int64(1), int64(1), int64(1)}, "Index": array{int64(0), int64(3)}},
		{"W": array{int64(1), int64(1), int64(1)}},
	} {
		value := testStream(t, nil, header)
		if _, err := readXrefStreamData(value.r, value.data.(stream), make([]xref, 2), 2); err == nil {
			t.Fatalf("invalid xref accepted: %v", header)
		}
	}
	value := testStream(t, []byte{42}, dict{"W": array{int64(0), int64(1), int64(0)}})
	table, err := readXrefStreamData(value.r, value.data.(stream), nil, 1)
	if err != nil || len(table) != 1 || table[0].offset != 42 {
		t.Fatalf("default xref type=%v %v", table, err)
	}
}

func TestXrefStreamRejectsUnrepresentableObjectCoordinates(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields [3]uint64
	}{
		{name: "offset", fields: [3]uint64{1, 1 << 63, 0}},
		{name: "generation", fields: [3]uint64{1, 1, 1 << 16}},
		{name: "object stream", fields: [3]uint64{2, 1 << 32, 0}},
		{name: "stream index", fields: [3]uint64{2, 1, 1 << 63}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := make([]byte, 24)
			for index, field := range test.fields {
				binary.BigEndian.PutUint64(data[index*8:], field)
			}
			value := testStream(t, data, dict{"W": array{int64(8), int64(8), int64(8)}})
			if _, err := readXrefStreamData(value.r, value.data.(stream), nil, 1); err == nil {
				t.Fatal("overflowing xref field was narrowed into another object")
			}
		})
	}
}

func TestXrefTableRejectsInvalidObjectCoordinates(t *testing.T) {
	for _, entry := range []string{"-1 0 n", "1 -1 n", "1 65536 n"} {
		input := newBuffer(strings.NewReader("1 1\n"+entry+"\ntrailer"), 0, parserBudget(t))
		if _, err := readXrefTableData(input, nil); err == nil {
			t.Fatalf("invalid coordinates accepted: %s", entry)
		}
	}
}

func TestObjectStreamsRejectInvalidOffsetsAndMissingObject(t *testing.T) {
	value := testStream(t, []byte("7 0 (value)"), dict{"Type": name("ObjStm"), "N": int64(1), "First": int64(4)})
	if _, found := value.r.streamObject(value, objptr{8, 0}); found {
		t.Fatal("unknown compressed object was fabricated")
	}
	for _, sample := range []struct {
		source string
		first  int64
	}{
		{"7 -1 (value)", 5},
		{"7 0 (value)", 0},
		{"4294967303 0 (value)", 13},
		{"-4294967289 0 (value)", 14},
		{"0 0 (value)", 4},
		{"7.0 0 (value)", 6},
		{"7 /bad (value)", 7},
		{"7 0.0 (value)", 6},
		{"8 -1 (value)", 5},
		{"7 9223372036854775807 (value)", 22},
	} {
		invalid := testStream(t, []byte(sample.source), dict{"N": int64(1), "First": sample.first})
		var panicked bool
		func() { defer func() { panicked = recover() != nil }(); invalid.r.streamObject(invalid, objptr{7, 0}) }()
		if !panicked {
			t.Fatalf("invalid compressed object coordinates accepted: %s", sample.source)
		}
	}
}
