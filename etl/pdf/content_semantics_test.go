package pdf_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/pdf"
)

func pdfStream(content, entries string) string {
	return fmt.Sprintf("<< /Length %d %s >>\nstream\n%s\nendstream", len(content), entries, content)
}

func extractPDFText(t *testing.T, objects []string) string {
	t.Helper()
	payload := encodePDFObjects(objects)
	reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := reader.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var text []string
	for _, doc := range docs {
		text = append(text, doc.Text)
	}
	return strings.Join(text, "\n")
}

func TestToUnicodeTakesPrecedenceOverSimpleFontEncoding(t *testing.T) {
	mapping := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CMapName /Scope def 1 begincodespacerange <00> <ff> endcodespacerange 1 beginbfchar <41> <03a9> endbfchar endcmap CMapName currentdict /CMap defineresource pop end end"
	for _, encoding := range []string{"/WinAnsiEncoding", "<< /BaseEncoding /WinAnsiEncoding /Differences [65 /Euro] >>", "/Identity-H", "/CustomEncoding"} {
		got := extractPDFText(t, []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("BT /F1 12 Tf (A) Tj ET", ""),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding " + encoding + " /ToUnicode 6 0 R >>",
			pdfStream(mapping, ""),
		})
		if got != "Ω" {
			t.Errorf("encoding=%s text=%q; want Ω", encoding, got)
		}
	}
}

func TestStandardLatinFontsUseTheirBuiltInEncoding(t *testing.T) {
	for _, font := range []string{"Helvetica", "Times-Roman", "Courier-BoldOblique"} {
		got := extractPDFText(t, []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("BT /F1 12 Tf <2760aed0> Tj ET", ""),
			"<< /Type /Font /Subtype /Type1 /BaseFont /" + font + " >>",
		})
		if got != "’‘ﬁ—" {
			t.Errorf("font=%s text=%q; want the font encoding, not PDFDocEncoding", font, got)
		}
	}
}

func TestUnsupportedFontMappingsReportTheirPageAndCause(t *testing.T) {
	for _, sample := range []struct{ font, diagnostic string }{
		{"/Subtype /Type0 /BaseFont /Custom /Encoding /Identity-H", "Identity-H"},
		{"/Subtype /Type1 /BaseFont /Helvetica /Encoding /CustomEncoding", "CustomEncoding"},
		{"/Subtype /Type1 /BaseFont /Helvetica /Encoding /MacExpertEncoding", "MacExpertEncoding"},
		{"/Subtype /Type1 /BaseFont /Custom", "Custom"},
		{"/Subtype /Type1 /BaseFont /Helvetica /FontDescriptor << /FontFile 6 0 R >>", "embedded built-in encoding"},
		{"/Subtype /Type1 /BaseFont /Helvetica /Encoding << /BaseEncoding /WinAnsiEncoding /Differences [65 /UnknownGlyph] >>", "UnknownGlyph"},
	} {
		payload := encodePDFObjects([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("BT /F1 12 Tf (A) Tj ET", ""),
			"<< /Type /Font " + sample.font + " >>",
			pdfStream("unparsed font program", ""),
		})
		for _, perPage := range []bool{false, true} {
			reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{PerPage: perPage})
			if err != nil {
				t.Fatal(err)
			}
			docs, err := reader.Read(t.Context())
			if len(docs) != 0 || !errors.Is(err, pdf.ErrPartialRead) || !errors.Is(err, pdf.ErrUnsupportedFontEncoding) {
				t.Fatalf("font=%q docs=%v error=%v", sample.font, docs, err)
			}
			if !strings.Contains(err.Error(), "page 1") || !strings.Contains(err.Error(), sample.diagnostic) {
				t.Fatalf("font failure lost its location or cause: %v", err)
			}
		}
	}
}

func TestUnsupportedUnicodeMapInheritanceCannotFallBackToFontEncoding(t *testing.T) {
	for _, sample := range []struct{ program, entries string }{
		{"/Base usecmap", ""}, {"", "/UseCMap /Base"},
	} {
		payload := encodePDFObjects([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("BT /F1 12 Tf (A) Tj ET", ""),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /ToUnicode 6 0 R >>",
			pdfStream(sample.program, sample.entries),
		})
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if docs, err := reader.Read(t.Context()); len(docs) != 0 || !errors.Is(err, pdf.ErrUnsupportedFontEncoding) || !errors.Is(err, pdf.ErrPartialRead) {
			t.Fatalf("unsupported CMap produced text: %v, %v", docs, err)
		}
	}
}

func TestEncodingDifferencesComposeWithTheirBaseEncoding(t *testing.T) {
	for _, sample := range []struct{ encoding, want string }{
		{"<< /BaseEncoding /WinAnsiEncoding >>", "€AB"},
		{"<< /BaseEncoding /WinAnsiEncoding /Differences [65 /Omega] >>", "€ΩB"},
		{"<< /BaseEncoding /MacRomanEncoding /Differences [65 /Euro] >>", "Ä€B"},
	} {
		got := extractPDFText(t, []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("BT /F1 12 Tf <804142> Tj ET", ""),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding " + sample.encoding + " >>",
		})
		if got != sample.want {
			t.Errorf("encoding=%s text=%q; want %q", sample.encoding, got, sample.want)
		}
	}
}

func TestGraphicsStateRestoresFontAcrossContentStreams(t *testing.T) {
	got := extractPDFText(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R /F2 7 0 R >> >> /Contents [4 0 R 5 0 R] >>",
		pdfStream("BT /F1 12 Tf <80> Tj ET q BT /F2 12 Tf <80> Tj ET ", ""),
		pdfStream("Q BT <80> Tj ET", ""),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>",
	})
	if got != "€\nÄ\n€" {
		t.Fatalf("text=%q; want font state saved and restored", got)
	}
}

func TestFormTextUsesScopedResourcesAndRestoresInheritedFont(t *testing.T) {
	got := extractPDFText(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 8 0 R >> /XObject << /Outer 5 0 R >> >> /Contents 4 0 R >>",
		pdfStream("BT /F1 12 Tf <80> Tj ET /Outer Do BT <80> Tj ET /Outer Do", ""),
		pdfStream("BT <80> Tj ET q BT /F1 12 Tf <80> Tj ET Q /Inner Do BT <80> Tj ET", "/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /Font << /F1 9 0 R >> /XObject << /Inner 6 0 R >> >>"),
		pdfStream("BT /F1 12 Tf <80> Tj ET", "/Type /XObject /Subtype /Form /BBox [0 0 100 100]"),
		"null",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>",
	})
	if want := "€\n€\nÄ\n€\n€\n€\n€\nÄ\n€\n€"; got != want {
		t.Fatalf("text=%q; want %q", got, want)
	}
}

func TestFormXObjectProvidesPageText(t *testing.T) {
	got := extractPDFText(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm 5 0 R >> >> /Contents 4 0 R >>",
		pdfStream("q /Fm Do Q", ""),
		pdfStream("BT /F1 12 Tf (FORM TEXT) Tj ET", "/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /Font << /F1 6 0 R >> >>"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	})
	if got != "FORM TEXT" {
		t.Fatalf("text=%q; want FORM TEXT", got)
	}
}

func TestInvalidContentStateCannotBecomeSuccessfulExtraction(t *testing.T) {
	for _, program := range []string{"Q", "q", "/Missing Do", "12 Do", "/Loop Do"} {
		payload := encodePDFObjects([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Loop 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream(program, ""),
			pdfStream("/Loop Do", "/Type /XObject /Subtype /Form /BBox [0 0 100 100]"),
		})
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if docs, err := reader.Read(t.Context()); len(docs) != 0 || !errors.Is(err, pdf.ErrPartialRead) {
			t.Errorf("program=%q docs=%v err=%v; want explicit page failure", program, docs, err)
		}
	}
}

func TestRepeatedFormExpansionUsesTheReadBudget(t *testing.T) {
	payload := encodePDFObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm 5 0 R >> >> /Contents 4 0 R >>",
		pdfStream(strings.Repeat("/Fm Do ", 20), ""),
		pdfStream("BT /F1 12 Tf ("+strings.Repeat("x", 100)+") Tj ET", "/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /Font << /F1 6 0 R >> >>"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	})
	assertParseBudget(t, payload, pdf.ParseBudget{MaxDecodedBytes: 4096})
}
