package document_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
)

func TestFormatterFuncDelegates(t *testing.T) {
	doc, err := document.NewDocument("hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	formatter := document.FormatterFunc(func(doc *document.Document) (string, error) {
		return strings.ToUpper(doc.Text), nil
	})
	if got, err := formatter.Format(doc); err != nil || got != "HI" {
		t.Fatalf("Format = %q, %v", got, err)
	}
}

func TestTextFormatterRendersTextAndRejectsWhatTextCannotCarry(t *testing.T) {
	formatter := document.TextFormatter{}
	doc, err := document.NewDocument("source text", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, formatErr := formatter.Format(doc); formatErr != nil || got != doc.Text {
		t.Fatalf("Format = %q, %v", got, formatErr)
	}
	for _, invalid := range []*document.Document{nil, {}, {Text: string([]byte{0xff})}} {
		if _, formatErr := formatter.Format(invalid); !errors.Is(formatErr, document.ErrInvalidDocument) {
			t.Fatalf("Format(%#v) error = %v, want ErrInvalidDocument", invalid, formatErr)
		}
	}
	payload, err := media.NewURI("image/png", "https://example.com/evidence.png")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "supporting text"} {
		withMedia, err := document.NewDocument(text, payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, formatErr := formatter.Format(withMedia); !errors.Is(formatErr, document.ErrUnsupportedMedia) {
			t.Fatalf("Format(media, %q) error = %v, want ErrUnsupportedMedia", text, formatErr)
		}
	}
}
