package chat_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestNewReasoningPartCopiesState(t *testing.T) {
	state := []byte("state")
	reasoning := chat.NewReasoningPart("thinking", state)
	state[0] = 'X'
	if string(reasoning.ReasoningState) != "state" {
		t.Fatalf("reasoning state = %q", reasoning.ReasoningState)
	}
}

func TestPartValidate(t *testing.T) {
	image := mustImage(t)
	valid := []chat.Part{
		chat.NewTextPart("hello"),
		chat.NewMediaPart(image),
		chat.NewReasoningPart("thinking", nil),
		chat.NewReasoningPart("", []byte("opaque")),
		chat.NewToolCallPart(validToolCall()),
		chat.NewToolResultPart(validToolResult()),
		chat.NewRefusalPart("cannot help"),
	}
	for _, part := range valid {
		if err := part.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", part, err)
		}
	}
}

func TestPartValidateRejectsInvalidAndAmbiguousValues(t *testing.T) {
	tests := []chat.Part{
		{},
		{Kind: "future", Text: "x"},
		{Kind: chat.PartText},
		{Kind: chat.PartText, Text: "x", ReasoningState: []byte("extra")},
		{Kind: chat.PartMedia},
		{Kind: chat.PartReasoning},
		{Kind: chat.PartToolCall},
		{Kind: chat.PartToolCall, ToolCall: &chat.ToolCall{Name: "tool", Arguments: `[]`}},
		{Kind: chat.PartToolResult},
		{Kind: chat.PartToolResult, ToolResult: &chat.ToolResult{ID: "call"}},
		{Kind: chat.PartRefusal},
	}
	for _, part := range tests {
		if err := part.Validate(); !errors.Is(err, chat.ErrInvalidPart) {
			t.Errorf("Validate(%+v) error = %v, want ErrInvalidPart", part, err)
		}
	}
}

func TestPartValidateRecursesIntoMedia(t *testing.T) {
	part := chat.NewMediaPart(&media.Media{})
	if err := part.Validate(); !errors.Is(err, media.ErrInvalidMIME) || !errors.Is(err, chat.ErrInvalidPart) {
		t.Fatalf("Validate error = %v, want media and part errors", err)
	}
}

func TestPartJSONRoundTrip(t *testing.T) {
	parts := []chat.Part{
		chat.NewTextPart("hello"),
		chat.NewMediaPart(mustImage(t)),
		chat.NewReasoningPart("thinking", []byte("signature")),
		chat.NewToolCallPart(validToolCall()),
		chat.NewToolResultPart(validToolResult()),
		chat.NewRefusalPart("cannot help"),
	}
	for _, part := range parts {
		encoded, err := json.Marshal(part)
		if err != nil {
			t.Fatalf("Marshal(%q): %v", part.Kind, err)
		}
		var got chat.Part
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatalf("Unmarshal(%q): %v", part.Kind, err)
		}
		if part.Kind == chat.PartMedia {
			if got.Kind != part.Kind || got.Media.MIME != part.Media.MIME || !reflect.DeepEqual(got.Media.Source, part.Media.Source) {
				t.Fatalf("media round trip = %#v, want %#v", got, part)
			}
			continue
		}
		if !reflect.DeepEqual(got, part) {
			t.Fatalf("round trip = %#v, want %#v", got, part)
		}
	}
}

func TestPartUnmarshalRejectsUnknownKindWithoutMutatingReceiver(t *testing.T) {
	got := chat.NewTextPart("keep")
	err := json.Unmarshal([]byte(`{"kind":"future","text":"replace"}`), &got)
	if !errors.Is(err, chat.ErrInvalidPart) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidPart", err)
	}
	if got.Kind != chat.PartText || got.Text != "keep" {
		t.Fatalf("failed Unmarshal mutated receiver: %+v", got)
	}
}

func TestPartNilUnmarshalReceiver(t *testing.T) {
	var part *chat.Part
	if err := part.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidPart) {
		t.Fatalf("UnmarshalJSON error = %v, want ErrInvalidPart", err)
	}
}

func TestPartKindValid(t *testing.T) {
	for _, kind := range []chat.PartKind{chat.PartText, chat.PartMedia, chat.PartReasoning, chat.PartToolCall, chat.PartToolResult, chat.PartRefusal} {
		if !kind.Valid() {
			t.Errorf("%q must be valid", kind)
		}
	}
	if chat.PartKind("future").Valid() {
		t.Fatal("future kind must be invalid")
	}
}

func TestPartRejectsMetadataOnlyTextCarrier(t *testing.T) {
	part := chat.Part{Kind: chat.PartText}
	if err := part.Metadata.Set("google/native_part", map[string]any{"thoughtSignature": "opaque"}); err != nil {
		t.Fatalf("set metadata: %v", err)
	}
	if err := part.Validate(); !errors.Is(err, chat.ErrInvalidPart) {
		t.Fatalf("Validate error = %v, want ErrInvalidPart", err)
	}

	invalid := chat.Part{Kind: chat.PartText, Text: "text", Metadata: metadata.Map{"bad": json.RawMessage(`{`)}}
	if err := invalid.Validate(); !errors.Is(err, metadata.ErrInvalidValue) {
		t.Fatalf("invalid metadata error = %v, want ErrInvalidValue", err)
	}
}

func TestCitationValidate(t *testing.T) {
	valid := []chat.Citation{
		{Source: chat.CitationSource{Kind: chat.CitationSourceURI, Value: "https://example.com/source"}},
		{Source: chat.CitationSource{Kind: chat.CitationSourceReference, Value: "document-7"}, Quote: "evidence"},
	}
	for _, citation := range valid {
		if err := citation.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", citation, err)
		}
	}
	invalid := []chat.Citation{
		{},
		{Source: chat.CitationSource{Kind: chat.CitationSourceURI, Value: "/relative"}},
		{Source: chat.CitationSource{Kind: chat.CitationSourceReference, Value: " reference "}},
		{Source: chat.CitationSource{Kind: chat.CitationSourceReference, Value: "reference"}, Title: " title "},
	}
	for _, citation := range invalid {
		if err := citation.Validate(); !errors.Is(err, chat.ErrInvalidCitation) {
			t.Errorf("Validate(%+v) error = %v, want ErrInvalidCitation", citation, err)
		}
	}
}

func TestCitationsBelongOnlyToText(t *testing.T) {
	for _, part := range []chat.Part{
		chat.NewMediaPart(mustImage(t)), chat.NewReasoningPart("reason", nil),
		chat.NewRefusalPart("refused"), chat.NewToolCallPart(validToolCall()),
		chat.NewToolResultPart(chat.ToolResult{ID: "call", Name: "tool"}),
	} {
		t.Run(string(part.Kind), func(t *testing.T) {
			for _, citation := range []chat.Citation{{}, {Source: chat.CitationSource{Kind: chat.CitationSourceReference, Value: "source"}}} {
				part.Citations = []chat.Citation{citation}
				if err := part.Validate(); !errors.Is(err, chat.ErrInvalidPart) {
					t.Fatalf("Validate = %v", err)
				}
				if _, err := json.Marshal(part); !errors.Is(err, chat.ErrInvalidPart) {
					t.Fatalf("Marshal = %v", err)
				}
			}
		})
	}
}
