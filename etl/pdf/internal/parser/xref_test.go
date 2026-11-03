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

func TestObjectStreamsRejectInvalidOffsetsAndMissingObject(t *testing.T) {
	value := testStream(t, []byte("7 0 (value)"), dict{"Type": name("ObjStm"), "N": int64(1), "First": int64(4)})
	if _, found := value.r.streamObject(value, objptr{8, 0}); found {
		t.Fatal("unknown compressed object was fabricated")
	}
	for _, sample := range []struct {
		source string
		first  int64
	}{{"7 -1 (value)", 5}, {"7 0 (value)", 0}} {
		invalid := testStream(t, []byte(sample.source), dict{"N": int64(1), "First": sample.first})
		var panicked bool
		func() { defer func() { panicked = recover() != nil }(); invalid.r.streamObject(invalid, objptr{7, 0}) }()
		if !panicked {
			t.Fatal("invalid compressed object offset was followed")
		}
	}
}
