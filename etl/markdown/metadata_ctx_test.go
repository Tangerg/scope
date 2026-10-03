package markdown_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl/markdown"
)

func TestConfigMetadataAppliedToEveryDocument(t *testing.T) {
	metadata := mustMetadata(t, map[string]any{"source": "manual.md", "tenant": "acme"})
	r, err := markdown.NewReader(strings.NewReader(sample),
		markdown.ReaderConfig{HeadingSplitLevel: 2, Metadata: metadata},
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
		t.Fatal("expected sections")
	}
	for i, d := range docs {
		source, _ := metadataValue[string](t, d.Metadata, "source")
		tenant, _ := metadataValue[string](t, d.Metadata, "tenant")
		if source != "manual.md" || tenant != "acme" {
			t.Fatalf("doc %d missing extra metadata: %v", i, d.Metadata)
		}
	}
}

func TestConfigMetadataRejectsReaderOwnedKeys(t *testing.T) {
	for _, key := range []string{markdown.MetadataHeading, markdown.MetadataHeadingLevel, markdown.MetadataHeadingPath, markdown.MetadataSourceName} {
		for _, level := range []int{0, 1} {
			reader, err := markdown.NewReader(strings.NewReader("Content without a heading"), markdown.ReaderConfig{
				HeadingSplitLevel: level, Metadata: mustMetadata(t, map[string]any{key: "forged"}),
			})
			if err == nil || reader != nil {
				t.Fatalf("metadata key %q supplied a reader-owned fact: %v", key, err)
			}
		}
	}
}

func TestConfigMetadataRejectsInvalidValueAtConstruction(t *testing.T) {
	_, err := markdown.NewReader(
		strings.NewReader(sample),
		markdown.ReaderConfig{Metadata: coremetadata.Map{"broken": []byte("{")}},
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
	r, _ := markdown.NewReader(strings.NewReader(sample), markdown.ReaderConfig{HeadingSplitLevel: 2})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Read(ctx); err == nil {
		t.Fatal("canceled context must produce an error")
	}
}
