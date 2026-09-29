package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl/pdf"
)

func TestRead_HonorsContextCancellation(t *testing.T) {
	metadata, err := coremetadata.FromValues(map[string]any{"source": "x.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := pdf.NewReader(bytes.NewReader([]byte("%PDF-1.4")), 8, pdf.ReaderConfig{Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: got %v, want context.Canceled", err)
	}
}

func TestConfigMetadataRejectsInvalidValueAtConstruction(t *testing.T) {
	_, err := pdf.NewReader(
		bytes.NewReader([]byte("%PDF-1.4")),
		8,
		pdf.ReaderConfig{Metadata: coremetadata.Map{"broken": []byte("{")}},
	)
	if !errors.Is(err, coremetadata.ErrInvalidValue) {
		t.Fatalf("NewReader error = %v, want ErrInvalidValue", err)
	}
}
