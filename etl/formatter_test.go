package etl_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
)

func TestMetadataHeaderFormatterIncludesMetadataByDefault(t *testing.T) {
	doc, _ := document.NewDocument("body", nil)
	_ = doc.Metadata.Set("k", "v")
	formatter := etl.NewMetadataHeaderFormatter(etl.MetadataHeaderFormatterConfig{})

	formatted, err := formatter.Format(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted, "k: v") || !strings.Contains(formatted, "body") {
		t.Fatalf("Format output: %q", formatted)
	}
}

func TestMetadataHeaderFormatterExcludesConfiguredKeys(t *testing.T) {
	doc, _ := document.NewDocument("body", nil)
	_ = doc.Metadata.Set("public", "yes")
	_ = doc.Metadata.Set("secret", "hidden")

	excluded := []string{"secret"}
	formatter := etl.NewMetadataHeaderFormatter(etl.MetadataHeaderFormatterConfig{
		ExcludedMetadata: excluded,
	})
	excluded[0] = "public"

	formatted, err := formatter.Format(doc)
	if err != nil {
		t.Fatal(err)
	}
	if formatted != "public: yes\n\nbody" {
		t.Fatalf("filtered output = %q", formatted)
	}
	if value, present, err := doc.Metadata.Decode[string]("secret"); err != nil || !present || value != "hidden" {
		t.Fatalf("caller metadata = %q, present %v, error %v", value, present, err)
	}
}

func TestMetadataHeaderFormatterPreservesTypedMetadataBoundary(t *testing.T) {
	doc, _ := document.NewDocument("body", nil)
	doc.Metadata = metadata.Map{
		"null":   []byte("null"),
		"number": []byte("9007199254740993"),
		"object": []byte(`{ "nested": true }`),
		"string": []byte(`"plain"`),
	}

	formatted, err := etl.NewMetadataHeaderFormatter(etl.MetadataHeaderFormatterConfig{}).Format(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := "null: \nnumber: 9007199254740993\nobject: {\"nested\":true}\nstring: plain\n\nbody"
	if formatted != want {
		t.Fatalf("Format = %q, want %q", formatted, want)
	}
}

func TestFormattersRejectInvalidDocuments(t *testing.T) {
	if _, err := etl.NewMetadataHeaderFormatter(etl.MetadataHeaderFormatterConfig{}).Format(nil); !errors.Is(err, etl.ErrNilDocument) {
		t.Fatalf("nil document error = %v, want ErrNilDocument", err)
	}
	payload, err := media.NewURI("image/png", "https://example.com/figure.png")
	if err != nil {
		t.Fatal(err)
	}
	withMedia, err := document.NewDocument("caption", payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, mediaErr := etl.NewMetadataHeaderFormatter(etl.MetadataHeaderFormatterConfig{}).Format(withMedia); !errors.Is(mediaErr, document.ErrUnsupportedMedia) {
		t.Fatalf("media document error = %v, want ErrUnsupportedMedia", mediaErr)
	}

	doc, _ := document.NewDocument("body", nil)
	doc.Metadata = metadata.Map{"broken": []byte("{")}
	_, err = etl.NewMetadataHeaderFormatter(etl.MetadataHeaderFormatterConfig{}).Format(doc)
	if !errors.Is(err, metadata.ErrInvalidValue) {
		t.Fatalf("MetadataHeaderFormatter error = %v, want ErrInvalidValue", err)
	}
}
