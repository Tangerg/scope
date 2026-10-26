package pdf_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/pdf"
)

func TestUntrustedPageCountCannotBypassTheReadLimit(t *testing.T) {
	for _, perPage := range []bool{false, true} {
		for _, count := range []int{2, pdf.DefaultMaxPages + 1} {
			payload := encodePDFObjects([]string{
				`<< /Type /Catalog /Pages 2 0 R >>`,
				fmt.Sprintf(`<< /Type /Pages /Kids [] /Count %d >>`, count),
			})
			maximum := 1
			if count > pdf.DefaultMaxPages {
				maximum = 0
			}
			reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{PerPage: perPage, MaxPages: maximum})
			if err != nil {
				t.Fatal(err)
			}
			if docs, err := reader.Read(t.Context()); docs != nil || !errors.Is(err, pdf.ErrPageLimit) {
				t.Fatalf("perPage=%t count=%d Read = %v, %v", perPage, count, docs, err)
			}
		}
	}
}

func TestPageLimitAcceptsExactCountAndRejectsInvalidConfiguration(t *testing.T) {
	payload := testPDF("One", "Two")
	if reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{MaxPages: -1}); reader != nil || err == nil {
		t.Fatalf("negative limit: %v, %v", reader, err)
	}
	reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{MaxPages: 2, PerPage: true})
	if err != nil {
		t.Fatal(err)
	}
	if docs, err := reader.Read(t.Context()); len(docs) != 2 || err != nil {
		t.Fatalf("exact limit: %v, %v", docs, err)
	}
}

func TestNegativePageCountDoesNotBecomeEmptySuccess(t *testing.T) {
	payload := encodePDFObjects([]string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [] /Count -1 >>`,
	})
	reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if docs, err := reader.Read(t.Context()); docs != nil || err == nil {
		t.Fatalf("negative page count: %v, %v", docs, err)
	}
}

func TestMissingDeclaredPagesAreReported(t *testing.T) {
	payload := encodePDFObjects([]string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [] /Count 1 >>`,
	})
	for _, perPage := range []bool{false, true} {
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{PerPage: perPage})
		if err != nil {
			t.Fatal(err)
		}
		docs, err := reader.Read(t.Context())
		if len(docs) != 0 || !errors.Is(err, pdf.ErrPartialRead) {
			t.Fatalf("perPage=%t Read = %v, %v", perPage, docs, err)
		}
	}
}

func TestPartialReadPreservesReadablePages(t *testing.T) {
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 3 >>`,
		`<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 7 0 R >> >> /Contents 4 0 R >>`,
		"",
		`<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 7 0 R >> >> /Contents 6 0 R >>`,
		"",
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`,
	}
	for index, text := range []string{"Alpha", "Omega"} {
		content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", text)
		objects[3+2*index] = fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content)
	}
	payload := encodePDFObjects(objects)
	for _, perPage := range []bool{false, true} {
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{PerPage: perPage})
		if err != nil {
			t.Fatal(err)
		}
		docs, err := reader.Read(t.Context())
		if !errors.Is(err, pdf.ErrPartialRead) {
			t.Fatalf("perPage=%t Read error = %v", perPage, err)
		}
		var texts []string
		for _, doc := range docs {
			texts = append(texts, doc.Text)
			assertMetadata(t, doc.Metadata, pdf.MetadataPagesTotal, 3)
		}
		if got := strings.Join(texts, "\n\n"); got != "Alpha\n\nOmega" {
			t.Fatalf("perPage=%t extracted %q", perPage, got)
		}
	}
}
