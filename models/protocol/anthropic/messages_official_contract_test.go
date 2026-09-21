package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestNativeClaudeSamplingContract(t *testing.T) {
	dialect := Dialect{Provider: "anthropic", MaxTemperature: 1, RejectTopK: true, RejectTopP: true}
	tests := []struct {
		name    string
		options corechat.Options
		want    string
	}{
		{name: "top k", options: corechat.Options{TopK: new(int64(10))}, want: "top_k is not supported"},
		{name: "top p", options: corechat.Options{TopP: new(0.9)}, want: "top_p is not supported"},
		{name: "temperature", options: corechat.Options{Temperature: new(1.1)}, want: "between 0 and 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := validNativeRequest(t)
			request.Options = tt.options
			_, err := mapProtocolRequest(corechat.Options{Model: "claude-opus-4-6"}, request, dialect)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestNativeClaudeRejectsZeroMaxTokens(t *testing.T) {
	request := validNativeRequest(t)
	request.Options.MaxOutputTokens = new(int64(0))
	_, err := mapProtocolRequest(corechat.Options{Model: "claude-opus-4-6"}, request, Dialect{Provider: "anthropic"})
	if err == nil || !strings.Contains(err.Error(), "max_output_tokens must be greater than zero") {
		t.Fatalf("error = %v, want max_output_tokens validation failure", err)
	}
}

func TestNativeClaudeMapsPortableToolChoice(t *testing.T) {
	request := validNativeRequest(t)
	request.Tools = []corechat.ToolDefinition{{
		Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
	request.ToolChoice = &corechat.ToolChoice{
		Mode: corechat.ToolChoiceNamed, Name: "lookup", Parallelism: corechat.ToolParallelismSingle,
	}
	params, err := mapProtocolRequest(corechat.Options{Model: "claude-opus-4-6"}, request, Dialect{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		ToolChoice struct {
			Type                   string `json:"type"`
			Name                   string `json:"name"`
			DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if body.ToolChoice.Type != "tool" || body.ToolChoice.Name != "lookup" || !body.ToolChoice.DisableParallelToolUse {
		t.Fatalf("tool choice = %#v", body.ToolChoice)
	}
}

func TestNativeClaudePreservesServerToolResponse(t *testing.T) {
	message := &anthropicsdk.Message{
		ID:    "msg-server-tool",
		Model: "claude-opus-4-6",
		Content: []anthropicsdk.ContentBlockUnion{
			{Type: "server_tool_use", ID: "srvtoolu_1", Name: "web_search", Input: []byte(`{"query":"official docs"}`)},
			{Type: "text", Text: "result"},
		},
	}
	mapper := newProtocolStreamState("anthropic")
	payload, err := json.Marshal(map[string]any{"type": "message_start", "message": message})
	if err != nil {
		t.Fatal(err)
	}
	var event anthropicsdk.MessageStreamEventUnion
	if decodeErr := json.Unmarshal(payload, &event); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	response, err := mapper.mapEvent(event)
	if err != nil {
		t.Fatalf("mapProtocolMessage: %v", err)
	}
	if len(response.Parts) != 1 || response.Parts[0].Text != "result" {
		t.Fatalf("Core response = %#v", response.Parts)
	}
	preserved, found, err := response.Metadata.Extra.Decode[anthropicsdk.MessageStreamEventUnion](StreamEventExtensionKey)
	if err != nil || !found {
		t.Fatalf("decode native response = found %v, error %v", found, err)
	}
	if len(preserved.Message.Content) != 2 || preserved.Message.Content[0].Type != "server_tool_use" || preserved.Message.Content[0].ID != "srvtoolu_1" {
		t.Fatalf("preserved content = %#v", preserved.Message.Content)
	}
}

func TestProtocolCitationProjectionCoversOfficialVariants(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		wantKind  corechat.CitationSourceKind
		wantValue string
	}{
		{
			name: "character location", value: anthropicsdk.CitationCharLocation{
				FileID: "file-char", DocumentTitle: "Character document", CitedText: "character quote",
			},
			wantKind: corechat.CitationSourceReference, wantValue: "file-char",
		},
		{
			name: "page location title fallback", value: anthropicsdk.CitationPageLocation{
				DocumentTitle: "Page document", CitedText: "page quote",
			},
			wantKind: corechat.CitationSourceReference, wantValue: "Page document",
		},
		{
			name: "content block location", value: anthropicsdk.CitationContentBlockLocation{
				FileID: "file-block", DocumentTitle: "Block document", CitedText: "block quote",
			},
			wantKind: corechat.CitationSourceReference, wantValue: "file-block",
		},
		{
			name: "web search", value: anthropicsdk.CitationsWebSearchResultLocation{
				URL: "https://example.com/source", Title: "Web source", CitedText: "web quote",
			},
			wantKind: corechat.CitationSourceURI, wantValue: "https://example.com/source",
		},
		{
			name: "search result", value: anthropicsdk.CitationsSearchResultLocation{
				Source: "search-result-1", Title: "Search source", CitedText: "search quote",
			},
			wantKind: corechat.CitationSourceReference, wantValue: "search-result-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			citation, include, err := mapProtocolCitation(tt.value)
			if err != nil || !include {
				t.Fatalf("mapProtocolCitation = %#v, %v, %v", citation, include, err)
			}
			if citation.Source.Kind != tt.wantKind || citation.Source.Value != tt.wantValue {
				t.Fatalf("citation source = %#v", citation.Source)
			}
			if err := citation.Validate(); err != nil {
				t.Fatalf("projected citation is invalid: %v", err)
			}
		})
	}

	for name, value := range map[string]any{
		"document without identity": anthropicsdk.CitationCharLocation{},
		"search without source":     anthropicsdk.CitationsSearchResultLocation{},
		"unsupported value":         struct{}{},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mapProtocolCitation(value); err == nil {
				t.Fatal("mapProtocolCitation accepted invalid citation")
			}
		})
	}
	if citation, include, err := mapProtocolCitation(nil); err != nil || include || citation != (corechat.Citation{}) {
		t.Fatalf("nil citation = %#v, %v, %v", citation, include, err)
	}
}

func TestProtocolPartsAsDeltasPreservesPortableSemantics(t *testing.T) {
	text := corechat.NewTextPart("answer")
	text.Citations = []corechat.Citation{{
		Source: corechat.CitationSource{Kind: corechat.CitationSourceURI, Value: "https://example.com/source"},
	}}
	reasoning := corechat.NewReasoningPart("thinking", []byte("state"))
	if err := reasoning.Metadata.Set("anthropic/test", true); err != nil {
		t.Fatal(err)
	}
	parts := []corechat.Part{
		text,
		reasoning,
		corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{}`}),
		corechat.NewRefusalPart("cannot answer"),
	}
	deltas, err := protocolPartsAsDeltas(parts)
	if err != nil {
		t.Fatal(err)
	}
	want := []corechat.PartDeltaKind{
		corechat.PartDeltaText,
		corechat.PartDeltaCitation,
		corechat.PartDeltaReasoning,
		corechat.PartDeltaToolCall,
		corechat.PartDeltaRefusal,
	}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %#v", deltas)
	}
	for index := range want {
		if deltas[index].Kind != want[index] {
			t.Errorf("delta[%d] kind = %q, want %q", index, deltas[index].Kind, want[index])
		}
	}
	if len(deltas[2].ReasoningState) == 0 || len(deltas[2].Metadata) == 0 {
		t.Errorf("reasoning delta lost replay state: %#v", deltas[2])
	}

	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocolPartsAsDeltas([]corechat.Part{corechat.NewMediaPart(image)}); err == nil {
		t.Fatal("protocolPartsAsDeltas accepted unsupported media")
	}
}

func TestReasoningReplayIsScopedToIssuingProvider(t *testing.T) {
	message := &anthropicsdk.Message{

		Content: []anthropicsdk.ContentBlockUnion{{Type: "thinking", Thinking: "private", Signature: "provider-signature"}},
	}
	parts, err := mapProtocolContent(message.Content, "minimax")
	if err != nil {
		t.Fatalf("mapProtocolMessage: %v", err)
	}
	assistant := corechat.NewAssistantMessage(parts...)

	matching, err := mapProtocolAssistant(assistant, "minimax")
	if err != nil || len(matching) != 1 || matching[0].GetSignature() == nil || *matching[0].GetSignature() != "provider-signature" {
		t.Fatalf("matching replay = %#v, error %v", matching, err)
	}
	foreign, err := mapProtocolAssistant(assistant, "anthropic")
	if err != nil {
		t.Fatalf("foreign replay: %v", err)
	}
	if len(foreign) != 0 {
		t.Fatalf("foreign provider received opaque reasoning: %#v", foreign)
	}
}

func validNativeRequest(t *testing.T) *corechat.Request {
	t.Helper()
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return request
}
