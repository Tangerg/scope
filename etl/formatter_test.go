package etl_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
)

func TestFormatterFunc(t *testing.T) {
	doc, _ := document.NewDocument("hi", nil)
	formatter := etl.FormatterFunc(func(doc *document.Document) (string, error) {
		return strings.ToUpper(doc.Text), nil
	})

	if got, _ := formatter.Format(doc); got != "HI" {
		t.Fatalf("Format = %q", got)
	}
}

func TestTextFormatterReturnsDocumentText(t *testing.T) {
	doc, _ := document.NewDocument("hi", nil)
	got, err := (etl.TextFormatter{}).Format(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi" {
		t.Fatalf("Format = %q, want hi", got)
	}
}

func TestSimpleFormatterIncludesMetadataByDefault(t *testing.T) {
	doc, _ := document.NewDocument("body", nil)
	_ = doc.Metadata.Set("k", "v")
	formatter := etl.NewSimpleFormatter(etl.SimpleFormatterConfig{})

	formatted, err := formatter.Format(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted, "k: v") || !strings.Contains(formatted, "body") {
		t.Fatalf("Format output: %q", formatted)
	}
}

func TestSimpleFormatterExcludesConfiguredKeys(t *testing.T) {
	doc, _ := document.NewDocument("body", nil)
	_ = doc.Metadata.Set("public", "yes")
	_ = doc.Metadata.Set("secret", "hidden")

	excluded := []string{"secret"}
	formatter := etl.NewSimpleFormatter(etl.SimpleFormatterConfig{
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

func TestSimpleFormatterPreservesTypedMetadataBoundary(t *testing.T) {
	doc, _ := document.NewDocument("body", nil)
	doc.Metadata = metadata.Map{
		"null":   []byte("null"),
		"number": []byte("9007199254740993"),
		"object": []byte(`{ "nested": true }`),
		"string": []byte(`"plain"`),
	}

	formatted, err := etl.NewSimpleFormatter(etl.SimpleFormatterConfig{}).Format(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := "null: \nnumber: 9007199254740993\nobject: {\"nested\":true}\nstring: plain\n\nbody"
	if formatted != want {
		t.Fatalf("Format = %q, want %q", formatted, want)
	}
}

func TestFormattersRejectInvalidDocuments(t *testing.T) {
	if _, err := (etl.TextFormatter{}).Format(nil); !errors.Is(err, etl.ErrNilDocument) {
		t.Fatalf("TextFormatter error = %v, want ErrNilDocument", err)
	}

	doc, _ := document.NewDocument("body", nil)
	doc.Metadata = metadata.Map{"broken": []byte("{")}
	_, err := etl.NewSimpleFormatter(etl.SimpleFormatterConfig{}).Format(doc)
	if !errors.Is(err, metadata.ErrInvalidValue) {
		t.Fatalf("SimpleFormatter error = %v, want ErrInvalidValue", err)
	}
}
