package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/pdf"
)

type interruptedSource struct {
	source io.ReaderAt
	cancel context.CancelFunc
	panic  bool
}

func (i interruptedSource) ReadAt(data []byte, offset int64) (int, error) {
	if i.panic {
		panic("corrupt trailer")
	}
	i.cancel()
	return i.source.ReadAt(data, offset)
}

func TestParserInterruptionDoesNotReturnDocuments(t *testing.T) {
	payload := testPDF("unpublished")
	for _, password := range []string{"", "unused"} {
		for _, panics := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			source := interruptedSource{source: bytes.NewReader(payload), cancel: cancel, panic: panics}
			reader, err := pdf.NewReader(source, int64(len(payload)), pdf.ReaderConfig{Password: password})
			if err != nil {
				t.Fatal(err)
			}
			docs, err := reader.Read(ctx)
			cancel()
			if docs != nil || err == nil {
				t.Fatalf("password=%q panic=%t Read = %v, %v", password, panics, docs, err)
			}
			if !panics && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		}
	}
}

func TestPasswordDoesNotPreventReadingAnUnencryptedPDF(t *testing.T) {
	payload := testPDF("public text")
	reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{Password: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if docs, err := reader.Read(t.Context()); err != nil || len(docs) != 1 || docs[0].Text != "public text" {
		t.Fatalf("Read = %v, %v", docs, err)
	}
}

func TestMalformedPageIsReportedAndBlankPageIsSkipped(t *testing.T) {
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>`,
		`<< /Type /Page /Parent 2 0 R /Resources << >> /Contents 5 0 R >>`,
		`<< /Type /Page /Parent 2 0 R /Resources << >> >>`,
		"<< /Length 3 >>\nstream\nTf\nendstream",
	}
	payload := encodePDFObjects(objects)
	for _, perPage := range []bool{false, true} {
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{PerPage: perPage})
		if err != nil {
			t.Fatal(err)
		}
		if docs, err := reader.Read(t.Context()); len(docs) != 0 || !errors.Is(err, pdf.ErrPartialRead) {
			t.Fatalf("perPage=%t Read = %v, %v", perPage, docs, err)
		}
	}
}

func TestInvalidSourceMetadataCannotEscapeInDocuments(t *testing.T) {
	payload := testPDF("readable")
	for _, perPage := range []bool{false, true} {
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{PerPage: perPage, SourceName: "bad\xff"})
		if err != nil {
			t.Fatal(err)
		}
		if docs, err := reader.Read(t.Context()); docs != nil || err == nil || !strings.Contains(err.Error(), "metadata") {
			t.Fatalf("perPage=%t Read = %v, %v", perPage, docs, err)
		}
	}
}
