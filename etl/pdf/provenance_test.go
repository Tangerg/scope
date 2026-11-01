package pdf_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl/pdf"
)

func TestPageProvenanceHashesWholeBoundedOriginal(t *testing.T) {
	data := testPDF("Alpha", "Beta")
	facts := metadata.Map{}
	_ = facts.Set("pdf.page", "business")
	_ = facts.Set("pdf.source", "client")
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)), pdf.ReaderConfig{SourceName: "book.pdf", PerPage: true, Metadata: facts})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := r.Read(t.Context())
	if err != nil || len(ds) != 2 {
		t.Fatalf("read: %v %v", ds, err)
	}
	for i, d := range ds {
		p := d.Provenance
		if p == nil || p.Source != "book.pdf" || p.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) || p.Page != i+1 || p.Bytes != nil {
			t.Fatalf("page %+v", p)
		}
		if v, _, _ := d.Metadata.Decode[string]("pdf.page"); v != "business" {
			t.Fatal(d.Metadata)
		}
	}
	r, err = pdf.NewReader(bytes.NewReader(data), int64(len(data))+1, pdf.ReaderConfig{SourceName: "book.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if ds, err = r.Read(t.Context()); !errors.Is(err, io.ErrUnexpectedEOF) || ds != nil {
		t.Fatalf("short snapshot: %+v %v", ds, err)
	}
}
