package anthropic

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestNativeClaudeSamplingContract(t *testing.T) {
	dialect := Dialect{Provider: "anthropic"}
	tests := []struct {
		name    string
		options corechat.Options
		want    string
	}{
		{name: "top k", options: corechat.Options{TopK: new(int64(10))}, want: "top_k is not supported"},
		{name: "top p", options: corechat.Options{TopP: new(0.9)}, want: "top_p is not supported"},
		{name: "temperature", options: corechat.Options{Temperature: new(1.0)}, want: "temperature is not supported"},
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

func TestNativeClaudeRejectsCompetingStreamControl(t *testing.T) {
	for _, value := range []json.RawMessage{json.RawMessage(`true`), json.RawMessage(`false`), json.RawMessage(`null`)} {
		t.Run(string(value), func(t *testing.T) {
			request := validNativeRequest(t)
			if err := request.Options.Extensions.Set(RequestExtensionKey, map[string]json.RawMessage{"stream": value}); err != nil {
				t.Fatal(err)
			}
			if _, err := mapProtocolRequest(corechat.Options{Model: "claude-opus-4-6"}, request, Dialect{Provider: "anthropic"}); err == nil || !errors.Is(err, metadata.ErrCoreOwnedField) {
				t.Fatalf("stream admission error = %v", err)
			}
		})
	}
}

func TestNativeClaudeMapsPortableToolChoice(t *testing.T) {
	request := validNativeRequest(t)
	request.Tools = []toolcontract.Definition{{
		Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
	request.ToolChoice = &corechat.ToolChoice{
		Mode: corechat.ToolChoiceNamed, Name: "lookup", Parallelism: corechat.ToolParallelismSingle,
	}
	projection, err := mapProtocolRequest(corechat.Options{Model: "claude-opus-4-6"}, request, Dialect{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(projection.params)
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
	if err := jsonv2.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if body.ToolChoice.Type != "tool" || body.ToolChoice.Name != "lookup" || !body.ToolChoice.DisableParallelToolUse {
		t.Fatalf("tool choice = %#v", body.ToolChoice)
	}
}

func TestProtocolCitationProjectionCoversOfficialVariants(t *testing.T) {
	documents := protocolDocumentSources{
		{Kind: corecontent.CitationSourceReference, Value: "file-char"},
		{Kind: corecontent.CitationSourceURI, Value: "https://example.com/page.pdf"},
		{Kind: corecontent.CitationSourceReference, Value: "file-block"},
	}
	tests := []struct {
		name      string
		raw       string
		wantKind  corecontent.CitationSourceKind
		wantValue string
	}{
		{
			name:     "character location",
			raw:      `{"type":"char_location","document_index":0,"document_title":"Character document","file_id":"remote-file","cited_text":"character quote","start_char_index":0,"end_char_index":15}`,
			wantKind: corecontent.CitationSourceReference, wantValue: "file-char",
		},
		{
			name:     "page location without title",
			raw:      `{"type":"page_location","document_index":1,"document_title":null,"file_id":null,"cited_text":"page quote","start_page_number":1,"end_page_number":2}`,
			wantKind: corecontent.CitationSourceURI, wantValue: "https://example.com/page.pdf",
		},
		{
			name:     "content block location",
			raw:      `{"type":"content_block_location","document_index":2,"document_title":"Block document","file_id":null,"cited_text":"block quote","start_block_index":0,"end_block_index":1}`,
			wantKind: corecontent.CitationSourceReference, wantValue: "file-block",
		},
		{
			name:     "web search",
			raw:      `{"type":"web_search_result_location","url":"https://example.com/source","title":"Web source","cited_text":"web quote","encrypted_index":"index"}`,
			wantKind: corecontent.CitationSourceURI, wantValue: "https://example.com/source",
		},
		{
			name:     "search result",
			raw:      `{"type":"search_result_location","source":"search-result-1","title":"Search source","cited_text":"search quote","search_result_index":0,"start_block_index":0,"end_block_index":1}`,
			wantKind: corecontent.CitationSourceReference, wantValue: "search-result-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var native anthropicsdk.TextCitationUnion
			if err := jsonv2.Unmarshal([]byte(tt.raw), &native); err != nil {
				t.Fatal(err)
			}
			citation, include, err := mapProtocolTextCitation(native, documents)
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
		"document without index": anthropicsdk.CitationCharLocation{},
		"search without source":  anthropicsdk.CitationsSearchResultLocation{},
		"unsupported value":      struct{}{},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mapProtocolCitation(value, documents); err == nil {
				t.Fatal("mapProtocolCitation accepted invalid citation")
			}
		})
	}
	if citation, include, err := mapProtocolCitation(nil, documents); err != nil || include || citation != (corecontent.Citation{}) {
		t.Fatalf("nil citation = %#v, %v, %v", citation, include, err)
	}
}

func TestProtocolPartsAsDeltasPreservesPortableSemantics(t *testing.T) {
	text := corechat.NewTextPart("answer")
	text.Citations = []corecontent.Citation{{
		Source: corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: "https://example.com/source"},
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
	parts, err := mapProtocolContent(message.Content, "minimax", nil)
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
