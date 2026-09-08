package protocol

import (
	"bytes"
	"testing"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestProtocolMetadataUsesEndpointNamespace(t *testing.T) {
	mapped, err := newProtocolResponseMapper("vertexai").mapResponse("gemini", &genai.GenerateContentResponse{
		ResponseID: "response-1",
		Candidates: []*genai.Candidate{{
			Index:        0,
			Content:      &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "done"}}},
			FinishReason: genai.FinishReasonStop,
		}},
	})
	if err != nil {
		t.Fatalf("mapResponse: %v", err)
	}
	if _, found := mapped.Metadata.Extra["vertexai/response"]; !found {
		t.Fatal("response metadata does not use the endpoint namespace")
	}
	if _, leaked := mapped.Metadata.Extra[ResponseExtensionKey]; leaked {
		t.Fatal("response metadata leaked the Google provider namespace")
	}
	if _, found := mapped.Output.Message.Parts[0].Metadata["vertexai/native_part"]; !found {
		t.Fatal("part metadata does not use the endpoint namespace")
	}
}

func TestNativePartRoundTripPreservesThoughtSignaturePosition(t *testing.T) {
	tests := []struct {
		name string
		part *genai.Part
		kind corechat.PartKind
	}{
		{
			name: "function call",
			part: &genai.Part{
				FunctionCall:     &genai.FunctionCall{Name: "lookup", Args: map[string]any{"id": float64(7)}},
				ThoughtSignature: []byte("signed-tool-state"),
			},
			kind: corechat.PartToolCall,
		},
		{
			name: "answer text",
			part: &genai.Part{
				Text:             "answer",
				ThoughtSignature: []byte("signed-answer-state"),
			},
			kind: corechat.PartText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			corePart, include, err := mapProtocolCandidatePart("google", 3, tt.part)
			if err != nil {
				t.Fatalf("map response part: %v", err)
			}
			if !include {
				t.Fatal("semantic part was not mapped")
			}
			if corePart.Kind != tt.kind {
				t.Fatalf("Core kind = %q, want %q", corePart.Kind, tt.kind)
			}

			replayed, err := mapProtocolAssistantParts("google", []corechat.Part{corePart})
			if err != nil {
				t.Fatalf("map request part: %v", err)
			}
			if len(replayed) != 1 {
				t.Fatalf("replayed parts = %d, want 1", len(replayed))
			}
			if !bytes.Equal(replayed[0].ThoughtSignature, tt.part.ThoughtSignature) {
				t.Fatalf("thought signature = %q, want %q", replayed[0].ThoughtSignature, tt.part.ThoughtSignature)
			}
			if (replayed[0].FunctionCall == nil) != (tt.part.FunctionCall == nil) || replayed[0].Text != tt.part.Text {
				t.Fatalf("replayed part = %#v, want %#v", replayed[0], tt.part)
			}
		})
	}
}

func TestProtocolOnlyPartRemainsInNativeResponse(t *testing.T) {
	part := &genai.Part{ThoughtSignature: []byte("signed-empty-state")}
	_, include, err := mapProtocolCandidatePart("google", 3, part)
	if err != nil {
		t.Fatal(err)
	}
	if include {
		t.Fatal("provider-only part was promoted to a false Core semantic part")
	}
}

func TestProtocolToolChoiceUsesOneCoreSurface(t *testing.T) {
	config := &genai.GenerateContentConfig{}
	choice := &corechat.ToolChoice{
		Mode: corechat.ToolChoiceNamed, Name: "lookup", Parallelism: corechat.ToolParallelismAllow,
	}
	if err := mapProtocolToolChoice(choice, config); err != nil {
		t.Fatal(err)
	}
	functionCalling := config.ToolConfig.FunctionCallingConfig
	if functionCalling.Mode != genai.FunctionCallingConfigModeAny || len(functionCalling.AllowedFunctionNames) != 1 || functionCalling.AllowedFunctionNames[0] != "lookup" {
		t.Fatalf("function calling config = %#v", functionCalling)
	}
	choice.Parallelism = corechat.ToolParallelismSingle
	if err := mapProtocolToolChoice(choice, &genai.GenerateContentConfig{}); err == nil {
		t.Fatal("single parallelism was silently ignored")
	}
}

func TestProtocolMapsCitationMetadata(t *testing.T) {
	response, err := newProtocolResponseMapper("google").mapResponse("gemini", &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content:      &genai.Content{Parts: []*genai.Part{{Text: "Grounded answer."}}},
			FinishReason: genai.FinishReasonStop,
			CitationMetadata: &genai.CitationMetadata{Citations: []*genai.Citation{{
				URI: "https://example.com/source", Title: "Source",
			}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	citations := response.Output.Message.Parts[0].Citations
	if len(citations) != 1 || citations[0].Source.Value != "https://example.com/source" || citations[0].Title != "Source" {
		t.Fatalf("citations = %#v", citations)
	}
}

func TestProtocolToolCompletionPreservesProviderOutcome(t *testing.T) {
	for _, tc := range []struct {
		reason genai.FinishReason
		want   corechat.FinishReason
	}{
		{genai.FinishReasonStop, corechat.FinishReasonToolCalls},
		{genai.FinishReasonMaxTokens, corechat.FinishReasonLength},
		{genai.FinishReasonSafety, corechat.FinishReasonContentFilter},
		{genai.FinishReasonMalformedFunctionCall, corechat.FinishReasonOther},
		{genai.FinishReasonUnexpectedToolCall, corechat.FinishReasonOther},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			content := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "inspect", Args: map[string]any{}}}}}
			response, err := newProtocolResponseMapper("google").mapResponse("gemini", &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: content, FinishReason: tc.reason}}})
			if err != nil {
				t.Fatal(err)
			}
			if response.Output.FinishReason != tc.want {
				t.Fatalf("Call finish = %s, want %s", response.Output.FinishReason, tc.want)
			}
			mapper := newProtocolResponseMapper("google")
			first, err := mapper.mapDelta("gemini", &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: content}}})
			if err != nil {
				t.Fatal(err)
			}
			if first.FinishReason != "" {
				t.Fatal("unfinished tool chunk received a finish reason")
			}
			terminal, err := mapper.mapDelta("gemini", &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{FinishReason: tc.reason}}})
			if err != nil {
				t.Fatal(err)
			}
			terminal, err = mapper.complete(terminal)
			if err != nil {
				t.Fatal(err)
			}
			var accumulator corechat.ResponseAccumulator
			for _, delta := range []*corechat.ResponseDelta{first, terminal} {
				if addErr := accumulator.Add(delta); addErr != nil {
					t.Fatal(addErr)
				}
			}
			streamed, err := accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			if streamed.Output.FinishReason != tc.want {
				t.Fatalf("Stream finish = %s, want %s", streamed.Output.FinishReason, tc.want)
			}
			for _, output := range []*corechat.Output{response.Output, streamed.Output} {
				native, found, err := output.Metadata.Extra.Decode[genai.FinishReason]("google/native_finish_reason")
				if err != nil || !found || native != tc.reason {
					t.Fatalf("native reason = %s, found %v, error %v", native, found, err)
				}
			}
		})
	}
}
