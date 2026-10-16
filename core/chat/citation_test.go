package chat_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestCitationSourceURIIdentity(t *testing.T) {
	for _, value := range []string{
		"https://example.com/evidence#quote",
		"file:///repo/evidence.txt",
		"urn:isbn:9780141036144",
		"mailto:reader@example.com",
		"custom:/evidence",
	} {
		t.Run(value, func(t *testing.T) {
			citation := chat.Citation{Source: chat.CitationSource{Kind: chat.CitationSourceURI, Value: value}}
			if err := citation.Validate(); err != nil {
				t.Fatal(err)
			}
			part := chat.NewTextPart("evidence")
			part.Citations = []chat.Citation{citation}
			if err := part.Validate(); err != nil {
				t.Fatal(err)
			}
			encoded, err := jsonv2.Marshal(part)
			if err != nil {
				t.Fatal(err)
			}
			var restored chat.Part
			if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			if err := restored.Validate(); err != nil {
				t.Fatal(err)
			}
			if len(restored.Citations) != 1 || restored.Citations[0] != citation {
				t.Fatalf("citation changed: %+v", restored.Citations)
			}
		})
	}
	for _, value := range []string{
		"", "/repo/evidence.txt", "//example.com/evidence", "#quote",
		"urn:", "custom:?query", "custom:#quote", "https://example.com/%zz",
		" https://example.com", "https://example.com/a b", "urn:a\tb",
		"urn:a\u00a0b", "urn:\xff",
	} {
		t.Run(value, func(t *testing.T) {
			source := chat.CitationSource{Kind: chat.CitationSourceURI, Value: value}
			if err := source.Validate(); !errors.Is(err, chat.ErrInvalidCitation) {
				t.Fatalf("Validate() = %v; want ErrInvalidCitation", err)
			}
		})
	}
}
