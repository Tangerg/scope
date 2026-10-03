package html_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl/html"
)

func TestConfigMetadataAppliedToEveryDocument(t *testing.T) {
	metadata := mustMetadata(t, map[string]any{"source": "page.html", "tenant": "acme"})
	r, err := html.NewReader(strings.NewReader(samplePage),
		html.ReaderConfig{Metadata: metadata},
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata["source"][0] = 'x'
	docs, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("expected a document")
	}
	for i, d := range docs {
		if metadataValue[string](t, d.Metadata, "source") != "page.html" || metadataValue[string](t, d.Metadata, "tenant") != "acme" {
			t.Fatalf("doc %d missing extra metadata: %v", i, d.Metadata)
		}
	}
}

func TestConfigMetadataRejectsReaderOwnedKeys(t *testing.T) {
	for _, key := range []string{html.MetadataTitle, html.MetadataDescription, html.MetadataCanonical, html.MetadataSelector, html.MetadataSourceName} {
		reader, err := html.NewReader(strings.NewReader("<p>Content without page information</p>"), html.ReaderConfig{
			Metadata: mustMetadata(t, map[string]any{key: "forged"}),
		})
		if err == nil || reader != nil {
			t.Fatalf("metadata key %q supplied a reader-owned fact: %v", key, err)
		}
	}
}

func TestConfigMetadataRejectsInvalidValueAtConstruction(t *testing.T) {
	_, err := html.NewReader(
		strings.NewReader(samplePage),
		html.ReaderConfig{Metadata: coremetadata.Map{"broken": []byte("{")}},
	)
	if !errors.Is(err, coremetadata.ErrInvalidValue) {
		t.Fatalf("NewReader error = %v, want ErrInvalidValue", err)
	}
}

func mustMetadata(t *testing.T, values map[string]any) coremetadata.Map {
	t.Helper()
	metadata, err := coremetadata.FromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestRead_HonorsContextCancellation(t *testing.T) {
	r, _ := html.NewReader(strings.NewReader(samplePage), html.ReaderConfig{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Read(ctx); err == nil {
		t.Fatal("canceled context must produce an error")
	}
}
