package pdf_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/ascii85"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/pdf"
)

type xrefFuseSource struct {
	*bytes.Reader
	reads int
}

func (x *xrefFuseSource) ReadAt(data []byte, offset int64) (int, error) {
	x.reads++
	if x.reads > 32 {
		return 0, errors.New("xref test fuse reached")
	}
	return x.Reader.ReadAt(data, offset)
}

func TestCyclicXrefTerminatesWithoutExhaustingSource(t *testing.T) {
	payload := string(testPDF("one page"))
	start := strings.LastIndex(payload, "startxref\n")
	var xref int
	if _, err := fmt.Sscanf(payload[start:], "startxref\n%d", &xref); err != nil {
		t.Fatal(err)
	}
	insert := strings.Index(payload, "trailer\n<<") + len("trailer\n<<")
	payload = payload[:insert] + fmt.Sprintf(" /Prev %d ", xref) + payload[insert:]
	source := &xrefFuseSource{Reader: bytes.NewReader([]byte(payload))}
	reader, err := pdf.NewReader(source, int64(len(payload)), pdf.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if docs, err := reader.Read(t.Context()); docs != nil || err == nil || source.reads > 32 {
		t.Fatalf("Read = %v, %v; source reads = %d", docs, err, source.reads)
	}
}

func TestXrefSizeIsCheckedBeforeAllocation(t *testing.T) {
	var payload bytes.Buffer
	payload.WriteString("%PDF-1.7\n")
	xref := payload.Len()
	payload.WriteString("1 0 obj\n<< /Type /XRef /Size 1099511627776 /W [1 4 2] /Length 0 >>\nstream\n\nendstream\nendobj\n")
	fmt.Fprintf(&payload, "startxref\n%d\n%%%%EOF\n", xref)
	assertParseBudget(t, payload.Bytes(), pdf.ParseBudget{})
}

func TestExpandedStreamAndPredictorAllocationAreBounded(t *testing.T) {
	for _, columns := range []int64{0, 1 << 40} {
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write([]byte(strings.Repeat(" ", 1<<20))); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		parameters := ""
		if columns != 0 {
			parameters = fmt.Sprintf(" /DecodeParms << /Predictor 12 /Columns %d >>", columns)
		}
		content := fmt.Sprintf("<< /Length %d /Filter /FlateDecode%s >>\nstream\n%sendstream", compressed.Len(), parameters, compressed.String())
		payload := encodePDFObjects([]string{
			`<< /Type /Catalog /Pages 2 0 R >>`,
			`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
			`<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 4 0 R >>`,
			content,
		})
		assertParseBudget(t, payload, pdf.ParseBudget{MaxDecodedBytes: 4096})
	}
}

func TestParseWorkBudgetIsPerReadAndCannotReturnPartialDocuments(t *testing.T) {
	payload := testPDF("first page", "second page")
	assertParseBudget(t, payload, pdf.ParseBudget{MaxWork: 32})
	for _, budget := range []pdf.ParseBudget{{MaxWork: -1}, {MaxDecodedBytes: -1}, {MaxObjects: -1}} {
		if reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{ParseBudget: budget}); reader != nil || err == nil {
			t.Fatalf("negative budget = %v, %v", reader, err)
		}
	}
}

func assertParseBudget(t *testing.T, payload []byte, budget pdf.ParseBudget) {
	t.Helper()
	for _, perPage := range []bool{false, true} {
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{PerPage: perPage, ParseBudget: budget})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if docs, err := reader.Read(t.Context()); docs != nil || !errors.Is(err, pdf.ErrParseBudget) {
				t.Fatalf("perPage=%t Read = %v, %v; want no documents and ErrParseBudget", perPage, docs, err)
			}
		}
	}
}

type pageCancellationSource struct {
	*bytes.Reader
	cancel     context.CancelFunc
	pageOffset int64
	canceled   bool
}

func (p *pageCancellationSource) ReadAt(data []byte, offset int64) (int, error) {
	if offset == p.pageOffset {
		p.canceled = true
		p.cancel()
	}
	return p.Reader.ReadAt(data, offset)
}

func TestCancellationInsidePageParsingIsNotPartialSuccess(t *testing.T) {
	payload := testPDF("one page")
	offset := int64(bytes.Index(payload, []byte("3 0 obj")))
	for _, perPage := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		source := &pageCancellationSource{Reader: bytes.NewReader(payload), cancel: cancel, pageOffset: offset}
		reader, err := pdf.NewReader(source, int64(len(payload)), pdf.ReaderConfig{PerPage: perPage})
		if err != nil {
			t.Fatal(err)
		}
		docs, err := reader.Read(ctx)
		cancel()
		if !source.canceled || docs != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Read = %v, %v; canceled during page = %t", docs, err, source.canceled)
		}
	}
}

func TestPageTreeCyclesAndCMapExpansionConsumeTheSameBudget(t *testing.T) {
	cycle := encodePDFObjects([]string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [2 0 R] /Count 1 >>`,
	})
	assertParseBudget(t, cycle, pdf.ParseBudget{MaxWork: 10_000})

	content := "BT /F1 12 Tf (A) Tj ET\n"
	cmap := "begincmap\n1000000000000 beginbfchar\nendbfchar\nendcmap\n"
	payload := encodePDFObjects([]string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>`,
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>`,
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(cmap), cmap),
	})
	assertParseBudget(t, payload, pdf.ParseBudget{})
}

func TestGlyphSequenceExpansionDiscardsAllDocumentsWhenBudgetIsExhausted(t *testing.T) {
	const repetitions = 1024
	content := "BT /F1 12 Tf (" + strings.Repeat("A", repetitions) + ") Tj ET"
	payload := encodePDFObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 2 /Kids [3 0 R 5 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 7 0 R >> >> /Contents 4 0 R >>",
		pdfStream("BT /F1 12 Tf (first page) Tj ET", ""),
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 8 0 R >> >> /Contents 6 0 R >>",
		pdfStream(content, ""),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /BaseEncoding /WinAnsiEncoding /Differences [65 /rehyehaleflamarabic] >> >>",
	})
	assertParseBudget(t, payload, pdf.ParseBudget{MaxDecodedBytes: 8 * repetitions})
}

func TestDecodedStreamBudgetCountsEachStageOnce(t *testing.T) {
	compress := func(content string) string {
		t.Helper()
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return compressed.String()
	}
	encodeASCII85 := func(content string) string {
		encoded := make([]byte, ascii85.MaxEncodedLen(len(content)))
		length := ascii85.Encode(encoded, []byte(content))
		return string(encoded[:length]) + "~>"
	}
	const text = "budgeted"
	const predictorRowBuffers = 2
	content := "BT /F1 12 Tf (" + text + ") Tj ET"
	compressed := compress(content)
	encoded := encodeASCII85(content)
	predictedContent := "\x02" + content
	predictedEncoded := "\x02" + encoded
	for _, sample := range []struct {
		name         string
		wire         string
		entries      string
		decodedBytes int
		bufferBytes  int
	}{
		{"unfiltered", content, "", len(content), 0},
		{"empty filter array", content, "/Filter []", len(content), 0},
		{"Flate", compressed, "/Filter /FlateDecode", len(content), 0},
		{"Flate array", compressed, "/Filter [/FlateDecode]", len(content), 0},
		{"Flate without predictor", compressed, "/Filter /FlateDecode /DecodeParms << /Predictor 1 >>", len(content), 0},
		{"ASCII85", encoded, "/Filter /ASCII85Decode", len(content), 0},
		{"ASCII85 then Flate", encodeASCII85(compressed), "/Filter [/ASCII85Decode /FlateDecode]", len(compressed) + len(content), 0},
		{"Flate then PNG-Up", compress(predictedContent), fmt.Sprintf("/Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns %d >>", len(content)), len(predictedContent) + len(content), predictorRowBuffers * len(predictedContent)},
		{"PNG-Up then ASCII85", compress(predictedEncoded), fmt.Sprintf("/Filter [/FlateDecode /ASCII85Decode] /DecodeParms [<< /Predictor 12 /Columns %d >> null]", len(encoded)), len(predictedEncoded) + len(encoded) + len(content), predictorRowBuffers * len(predictedEncoded)},
	} {
		t.Run(sample.name, func(t *testing.T) {
			payload := encodePDFObjects([]string{
				"<< /Type /Catalog /Pages 2 0 R >>",
				"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
				"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
				pdfStream(sample.wire, sample.entries),
				"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			})
			limit := int64(sample.decodedBytes + sample.bufferBytes + len(text) + len("\n"+text))
			for _, perPage := range []bool{false, true} {
				reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{
					PerPage:     perPage,
					ParseBudget: pdf.ParseBudget{MaxDecodedBytes: limit},
				})
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					docs, err := reader.Read(t.Context())
					if err != nil || len(docs) != 1 || docs[0].Text != text {
						t.Fatalf("perPage=%t limit=%d Read = %v, %v; want %q", perPage, limit, docs, err, text)
					}
				}
			}
			assertParseBudget(t, payload, pdf.ParseBudget{MaxDecodedBytes: limit - 1})
		})
	}
}
