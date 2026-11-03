package parser

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"io"
	"testing"
)

func literalValue(t *testing.T, data object) Value {
	t.Helper()
	return Value{r: &Reader{budget: parserBudget(t), resolving: make(map[objptr]bool)}, data: data}
}

func testStream(t *testing.T, source []byte, header dict) Value {
	t.Helper()
	if header == nil {
		header = make(dict)
	}
	header["Length"] = int64(len(source))
	reader := &Reader{f: bytes.NewReader(source), end: int64(len(source)), budget: parserBudget(t), resolving: make(map[objptr]bool)}
	return Value{r: reader, data: stream{hdr: header}}
}

func TestFontEncodingsRespectTheirDeclaredCharacterMaps(t *testing.T) {
	for _, sample := range []struct {
		encoding  object
		raw, want string
	}{
		{name("WinAnsiEncoding"), "\x80A", "€A"},
		{name("MacRomanEncoding"), "\x80A", "ÄA"},
		{name("UniGB-UCS2-H"), "\x00A\x00B", "AB"},
		{nil, "\x80A", "•A"},
		{name("Identity-H"), "AB", "AB"},
		{name("custom"), "AB", "AB"},
		{int64(5), "AB", "AB"},
		{dict{"Differences": array{int64(65), name("Euro"), name("UnknownGlyph"), int64(67), name("A")}}, "ABC", "€BA"},
	} {
		font := Font{V: literalValue(t, dict{"Encoding": sample.encoding})}
		encoder := font.Encoder()
		if got := encoder.Decode(sample.raw); got != sample.want {
			t.Fatalf("encoding=%v result=%q want=%q", sample.encoding, got, sample.want)
		}
		if font.Encoder() != encoder {
			t.Fatal("font did not reuse its frozen decoder")
		}
	}
}

func TestUnicodeCMapsSupportScalarAndArrayRangesAcrossByteBoundaries(t *testing.T) {
	for _, sample := range []struct{ mapping, raw, want string }{
		{"1 beginbfchar <0041> <0042> endbfchar", "\x00A", "B"},
		{"1 beginbfrange <0001> <0003> <0061> endbfrange", "\x00\x01\x00\x02\x00\x03", "abc"},
		{"1 beginbfrange <00fe> <0101> [<0041> <0042> <0043> <0044>] endbfrange", "\x00\xfe\x01\x00\x01\x01", "ACD"},
		{"1 beginbfrange <0001> <0001> [false] endbfrange", "\x00\x01", "�"},
		{"1 beginbfrange <0001> <0001> false endbfrange", "\x00\x01", "�"},
		{"", "\x00A", "�"},
		{"", "A", "�"},
	} {
		program := "/CIDInit /ProcSet findresource begin 12 dict begin /CMapName /Scope def 1 begincodespacerange <0000> <ffff> endcodespacerange " + sample.mapping + " CMapName currentdict /CMap defineresource pop end end"
		unicode := testStream(t, []byte(program), nil)
		font := Font{V: Value{r: unicode.r, data: dict{"Encoding": name("Identity-H"), "ToUnicode": unicode.data}}}
		if got := font.Encoder().Decode(sample.raw); got != sample.want {
			t.Fatalf("map=%s result=%q want=%q", sample.mapping, got, sample.want)
		}
	}
}

func TestFilteredStreamsHaveTheSameBytesUnderSmallReads(t *testing.T) {
	plain := []byte("text with spaces and exact bytes\n")
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := make([]byte, ascii85.MaxEncodedLen(len(plain)))
	count := ascii85.Encode(encoded, plain)
	encoded = append(encoded[:count], '~', '>')
	for _, sample := range []struct {
		wire   []byte
		header dict
	}{
		{plain, nil},
		{compressed.Bytes(), dict{"Filter": name("FlateDecode")}},
		{compressed.Bytes(), dict{"Filter": name("FlateDecode"), "DecodeParms": dict{"Predictor": int64(predictorNone)}}},
		{encoded, dict{"Filter": name("ASCII85Decode")}},
		{encoded, dict{"Filter": name("ASCII85Decode"), "DecodeParms": dict{}}},
		{compressed.Bytes(), dict{"Filter": array{name("FlateDecode")}, "DecodeParms": array{nil}}},
	} {
		source := testStream(t, sample.wire, sample.header).Reader()
		var result bytes.Buffer
		for {
			data := make([]byte, 3)
			n, err := source.Read(data)
			result.Write(data[:n])
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(result.Bytes(), plain) {
			t.Fatalf("decoded=%q want=%q", result.Bytes(), plain)
		}
	}
}

func TestFilterParametersCannotSilentlyBecomeDefaults(t *testing.T) {
	for _, parameters := range []object{true, int64(12), name("default")} {
		var err error
		func() {
			defer recoverTo(&err, func() {})
			testStream(t, []byte("encoded"), dict{"Filter": name("FlateDecode"), "DecodeParms": parameters}).Reader()
		}()
		if err == nil {
			t.Fatalf("invalid parameters silently ignored: %v", parameters)
		}
	}
}

func TestPNGUpReconstructsRowsAndRejectsOtherFilters(t *testing.T) {
	reader := &pngUpReader{r: bytes.NewReader([]byte{2, 1, 2, 3, 2, 3, 3, 3}), hist: make([]byte, 4), tmp: make([]byte, 4)}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("PNG-Up=%v %v", got, err)
	}
	invalid := &pngUpReader{r: bytes.NewReader([]byte{1, 2}), hist: make([]byte, 2), tmp: make([]byte, 2)}
	if _, err := io.ReadAll(invalid); err == nil {
		t.Fatal("another row filter was interpreted as PNG-Up")
	}
}

func TestResourceBudgetIncludesNestedLexingAndObjectResolution(t *testing.T) {
	grant := parserBudget(t)
	grant.depth = maxObjectDepth
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		newBuffer(bytes.NewReader([]byte("null")), 0, grant).readObject()
	}()
	cause, _ := recovered.(error)
	if !errors.Is(cause, ErrBudget) {
		t.Fatalf("nested parse escaped its shared depth budget: %v", recovered)
	}
}
