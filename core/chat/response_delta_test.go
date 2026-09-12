package chat_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestResponseDeltaJSONRoundTrip(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	if metadataErr := image.Metadata.Set("test/source", "fixture"); metadataErr != nil {
		t.Fatal(metadataErr)
	}
	citation := chat.Citation{
		Source: chat.CitationSource{Kind: chat.CitationSourceURI, Value: "https://example.com/source"},
		Title:  "Source",
	}
	delta := chat.ResponseDelta{
		Parts: []chat.PartDelta{
			chat.NewTextDelta("answer"),
			chat.NewCitationDelta(citation),
			chat.NewMediaDelta(image),
			chat.NewReasoningDelta("thinking", []byte("state")),
			chat.NewToolCallDelta(chat.ToolCallDelta{ID: "call-1", Name: "lookup", Arguments: "{"}),
			chat.NewRefusalDelta("cannot continue"),
		},
		FinishReason: chat.FinishReasonRefusal,
		Metadata:     &chat.ResponseMetadata{ID: "response-1", Model: "model"},
	}
	encoded, err := json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded chat.ResponseDelta
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, delta) {
		t.Fatalf("round trip = %#v, want %#v", decoded, delta)
	}
}

func TestResponseDeltaRejectsAmbiguousParts(t *testing.T) {
	invalid := []chat.PartDelta{
		{},
		{Kind: "future", Text: "x"},
		{Kind: chat.PartDeltaText},
		{Kind: chat.PartDeltaMedia, Text: "x"},
		{Kind: chat.PartDeltaReasoning},
		{Kind: chat.PartDeltaToolCall},
		{Kind: chat.PartDeltaCitation},
		{Kind: chat.PartDeltaRefusal},
	}
	for _, part := range invalid {
		if err := part.Validate(); !errors.Is(err, chat.ErrInvalidResponse) {
			t.Errorf("Validate(%+v) error = %v, want ErrInvalidResponse", part, err)
		}
	}
}

func TestResponseDeltaCloneOwnsNestedValues(t *testing.T) {
	state := []byte("state")
	delta := &chat.ResponseDelta{
		Parts:    []chat.PartDelta{chat.NewReasoningDelta("thinking", state)},
		Metadata: &chat.ResponseMetadata{Usage: &chat.Usage{OutputTokens: 1}},
	}
	clone := delta.Clone()
	clone.Parts[0].ReasoningState[0] = 'X'
	clone.Metadata.Usage.OutputTokens = 2
	if string(delta.Parts[0].ReasoningState) != "state" || delta.Metadata.Usage.OutputTokens != 1 {
		t.Fatalf("clone mutated source: %#v", delta)
	}
}

func TestResponseDeltaTextProjectsOnlyVisibleText(t *testing.T) {
	delta := &chat.ResponseDelta{Parts: []chat.PartDelta{
		chat.NewReasoningDelta("hidden", nil),
		chat.NewTextDelta("visible "),
		chat.NewRefusalDelta("refused"),
		chat.NewTextDelta("text"),
	}}
	if got := delta.Text(); got != "visible text" {
		t.Fatalf("Text = %q", got)
	}
	var nilDelta *chat.ResponseDelta
	if nilDelta.Text() != "" {
		t.Fatal("nil ResponseDelta.Text must be empty")
	}
}

func TestResponseDeltaUnmarshalIsAtomic(t *testing.T) {
	delta := chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("keep")}}
	if err := json.Unmarshal([]byte(`{"parts":[{"kind":"future","text":"replace"}]}`), &delta); !errors.Is(err, chat.ErrInvalidResponse) {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if delta.Parts[0].Text != "keep" {
		t.Fatalf("failed Unmarshal mutated receiver: %#v", delta)
	}
}
