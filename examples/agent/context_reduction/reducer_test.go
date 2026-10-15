package main

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func duplicateRequest() *chat.Request {
	messages := []chat.Message{chat.NewSystemMessage(contextRule), chat.NewUserMessage(chat.NewTextPart(contextGoal))}
	for _, id := range []string{"first", "second", "third"} {
		messages = append(messages,
			chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: id, Name: "read_report", Arguments: `{"index":0}`})),
			chat.NewToolMessage(chat.ToolResult{ID: id, Name: "read_report", Output: chat.NewTextToolOutput(strings.Repeat("evidence ", 256))}),
		)
	}
	return &chat.Request{Messages: messages,
		Tools:      []chat.ToolDefinition{{Name: "read_report", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &chat.ToolChoice{Mode: chat.ToolChoiceAuto}, Options: chat.Options{MaxOutputTokens: new(int64(128))},
	}
}

func TestDuplicateOutputReductionPreservesEvidenceAndPairing(t *testing.T) {
	request := duplicateRequest()
	before := request.Clone()
	reducer := duplicateToolOutputReducer{}
	messages, err := reducer.ReduceModelContext(t.Context(), interaction.ModelInvocation{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, before) {
		t.Fatal("reduction mutated its input")
	}
	expected := request.Clone()
	encoded, err := jsonv2.Marshal(duplicateOutputReference{ToolCallID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{5, 7} {
		expected.Messages[index].Parts[0].ToolResult.Output = chat.NewTextToolOutput(string(encoded))
	}
	if !reflect.DeepEqual(messages, expected.Messages) {
		t.Fatal("reduction changed more than duplicate output bodies")
	}
	effective := request.Clone()
	effective.Messages = messages
	if validateErr := effective.Validate(); validateErr != nil {
		t.Fatal(validateErr)
	}
	again, err := reducer.ReduceModelContext(t.Context(), interaction.ModelInvocation{}, effective)
	if err != nil || !reflect.DeepEqual(again, messages) {
		t.Fatalf("reduction is not idempotent: %v", err)
	}
	messages[0].Parts[0].Text = "changed"
	if request.Messages[0].Text() != contextRule {
		t.Fatal("reduction retained mutable input aliases")
	}
}

func TestDuplicateOutputReductionLeavesProtectedValuesIntact(t *testing.T) {
	attachment, err := media.NewURI("image/png", "https://example.com/evidence.png")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*chat.Request)
	}{
		{"different output", func(r *chat.Request) { r.Messages[5].Parts[0].ToolResult.Output.Content[0].Text += "new evidence" }},
		{"different tool", func(r *chat.Request) { r.Messages[5].Parts[0].ToolResult.Name = "another_reader" }},
		{"error", func(r *chat.Request) { r.Messages[5].Parts[0].ToolResult.IsError = true }},
		{"media", func(r *chat.Request) {
			r.Messages[5].Parts[0].ToolResult.Output.Content = []chat.ToolContent{{Kind: chat.PartMedia, Media: attachment}}
		}},
		{"structured output", func(r *chat.Request) {
			r.Messages[5].Parts[0].ToolResult.Output.Details = json.RawMessage(`{"source":"a"}`)
		}},
		{"multipart output", func(r *chat.Request) {
			r.Messages[5].Parts[0].ToolResult.Output.Content = append(r.Messages[5].Parts[0].ToolResult.Output.Content, chat.ToolContent{Kind: chat.PartText, Text: "another part"})
		}},
		{"citation", func(r *chat.Request) {
			r.Messages[5].Parts[0].ToolResult.Output.Content[0].Citations = []chat.Citation{{Source: chat.CitationSource{Kind: chat.CitationSourceReference, Value: "source:a"}}}
		}},
		{"content metadata", func(r *chat.Request) {
			r.Messages[5].Parts[0].ToolResult.Output.Content[0].Metadata = metadata.Map{"native": json.RawMessage(`true`)}
		}},
		{"part metadata", func(r *chat.Request) {
			r.Messages[5].Parts[0].Metadata = metadata.Map{"native": json.RawMessage(`true`)}
		}},
		{"message metadata", func(r *chat.Request) { r.Messages[5].Metadata = metadata.Map{"native": json.RawMessage(`true`)} }},
		{"small output", func(r *chat.Request) {
			for _, index := range []int{3, 5} {
				r.Messages[index].Parts[0].ToolResult.Output = chat.NewTextToolOutput("short")
			}
		}},
		{"ambiguous identity", func(r *chat.Request) { r.Messages[5].Parts[0].ToolResult.ID = "first" }},
		{"large reference", func(r *chat.Request) {
			r.Messages[3].Parts[0].ToolResult.ID = strings.Repeat("a", minimumDuplicateBytes)
		}},
		{"escaping expands reference", func(r *chat.Request) {
			id := strings.Repeat("\\", 450)
			r.Messages[2].Parts[0].ToolCall.ID, r.Messages[3].Parts[0].ToolResult.ID = id, id
			for _, index := range []int{3, 5} {
				r.Messages[index].Parts[0].ToolResult.Output = chat.NewTextToolOutput(strings.Repeat("x", minimumDuplicateBytes))
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := duplicateRequest()
			request.Messages = request.Messages[:6]
			test.change(request)
			messages, err := (duplicateToolOutputReducer{}).ReduceModelContext(t.Context(), interaction.ModelInvocation{}, request)
			if err != nil || !reflect.DeepEqual(messages, request.Messages) {
				t.Fatalf("protected evidence changed: %v", err)
			}
		})
	}
}

func TestDuplicateOutputReductionRejectsInvalidOrCanceledInput(t *testing.T) {
	reducer := duplicateToolOutputReducer{}
	if _, err := reducer.ReduceModelContext(t.Context(), interaction.ModelInvocation{}, nil); !errors.Is(err, chat.ErrInvalidRequest) {
		t.Fatalf("invalid input: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reducer.ReduceModelContext(ctx, interaction.ModelInvocation{}, duplicateRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input: %v", err)
	}
}

func BenchmarkDuplicateToolOutputReduction(b *testing.B) {
	request := duplicateRequest()
	request.Messages = request.Messages[:4]
	for index := 1; index < reportRounds; index++ {
		call, result := request.Messages[2].Clone(), request.Messages[3].Clone()
		call.Parts[0].ToolCall.ID = strings.Repeat("x", index)
		result.Parts[0].ToolResult.ID = call.Parts[0].ToolCall.ID
		request.Messages = append(request.Messages, call, result)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := (duplicateToolOutputReducer{}).ReduceModelContext(b.Context(), interaction.ModelInvocation{}, request); err != nil {
			b.Fatal(err)
		}
	}
}
