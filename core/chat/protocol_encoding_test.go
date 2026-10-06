package chat_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
)

type validatedProtocolValue interface {
	Validate() error
}

func TestProtocolValidationRejectsUnencodableStrings(t *testing.T) {
	tests := []struct {
		name     string
		value    func(string) validatedProtocolValue
		category error
	}{
		{"chat.ToolCall.ID", func(text string) validatedProtocolValue { return chat.ToolCall{ID: text, Name: "tool"} }, chat.ErrInvalidToolCall},
		{"chat.ToolCall.Name", func(text string) validatedProtocolValue { return chat.ToolCall{ID: "call", Name: text} }, chat.ErrInvalidToolCall},
		{"chat.ToolCall.Arguments", func(text string) validatedProtocolValue {
			return chat.ToolCall{ID: "call", Name: "tool", Arguments: text}
		}, chat.ErrInvalidToolCall},
		{"chat.ToolCallDelta.ID", func(text string) validatedProtocolValue { return chat.ToolCallDelta{ID: text, Name: "tool"} }, chat.ErrInvalidToolCall},
		{"chat.ToolCallDelta.Name", func(text string) validatedProtocolValue { return chat.ToolCallDelta{ID: "call", Name: text} }, chat.ErrInvalidToolCall},
		{"chat.ToolCallDelta.Arguments", func(text string) validatedProtocolValue {
			return chat.ToolCallDelta{ID: "call", Name: "tool", Arguments: text}
		}, chat.ErrInvalidToolCall},
		{"chat.ToolResult.ID", func(text string) validatedProtocolValue { return chat.ToolResult{ID: text, Name: "tool"} }, chat.ErrInvalidToolResult},
		{"chat.ToolResult.Name", func(text string) validatedProtocolValue { return chat.ToolResult{ID: "call", Name: text} }, chat.ErrInvalidToolResult},
		{"chat.Model", func(text string) validatedProtocolValue { return chat.Options{Model: text} }, chat.ErrInvalidOptions},
		{"chat.Stop", func(text string) validatedProtocolValue { return chat.Options{Stop: []string{text}} }, chat.ErrInvalidOptions},
		{"chat.Citation.URI", func(text string) validatedProtocolValue {
			return chat.CitationSource{Kind: chat.CitationSourceURI, Value: "https://example.test/" + text}
		}, chat.ErrInvalidCitation},
		{"chat.Citation.Reference", func(text string) validatedProtocolValue {
			return chat.CitationSource{Kind: chat.CitationSourceReference, Value: text}
		}, chat.ErrInvalidCitation},
		{"chat.Citation.Title", func(text string) validatedProtocolValue {
			return chat.Citation{Source: chat.CitationSource{Kind: chat.CitationSourceReference, Value: "source"}, Title: text}
		}, chat.ErrInvalidCitation},
		{"chat.Citation.Quote", func(text string) validatedProtocolValue {
			return chat.Citation{Source: chat.CitationSource{Kind: chat.CitationSourceReference, Value: "source"}, Quote: text}
		}, chat.ErrInvalidCitation},
		{"chat.OutputFormat.Name", func(text string) validatedProtocolValue {
			return chat.OutputFormat{Type: chat.OutputFormatJSONSchema, Name: text, Schema: []byte(`{"type":"object"}`)}
		}, chat.ErrInvalidOutputFormat},
		{"chat.OutputFormat.Description", func(text string) validatedProtocolValue {
			return chat.OutputFormat{Type: chat.OutputFormatJSONSchema, Name: "output", Description: text, Schema: []byte(`{"type":"object"}`)}
		}, chat.ErrInvalidOutputFormat},
		{"chat.Response.ID", func(text string) validatedProtocolValue {
			return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}, Metadata: &chat.ResponseMetadata{ID: text}}
		}, chat.ErrInvalidResponse},
		{"chat.Response.Model", func(text string) validatedProtocolValue {
			return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}, Metadata: &chat.ResponseMetadata{Model: text}}
		}, chat.ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := test.value("\xff")
			if err := invalid.Validate(); err == nil || (test.category != nil && !errors.Is(err, test.category)) {
				t.Errorf("Validate() = %v; want rejection with %v", err, test.category)
			}
			if _, err := jsonv2.Marshal(invalid); err == nil {
				t.Error("JSON encoding accepted invalid UTF-8")
			}
			valid := test.value("value-é-值")
			if err := valid.Validate(); err != nil {
				t.Fatalf("valid Unicode rejected: %v", err)
			}
			if _, err := jsonv2.Marshal(valid); err != nil {
				t.Fatalf("validated value cannot be encoded: %v", err)
			}
		})
	}
}

func TestProtocolValidationRejectsUnrepresentableTimestamps(t *testing.T) {
	value := func(createdAt time.Time) validatedProtocolValue {
		return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}, Metadata: &chat.ResponseMetadata{CreatedAt: createdAt}}
	}
	for _, createdAt := range []time.Time{
		time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("invalid", 24*60*60)),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("seconds", 43)),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("negative seconds", -43)),
	} {
		invalid := value(createdAt)
		if err := invalid.Validate(); !errors.Is(err, chat.ErrInvalidResponse) {
			t.Errorf("Validate(%v) = %v; want %v", createdAt, err, chat.ErrInvalidResponse)
		}
		if _, err := jsonv2.Marshal(invalid); err == nil {
			t.Errorf("JSON encoding accepted invalid timestamp %v", createdAt)
		}
	}
	for _, createdAt := range []time.Time{
		{},
		time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("valid", 23*60*60)),
	} {
		valid := value(createdAt)
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid timestamp %v rejected: %v", createdAt, err)
		}
		if _, err := jsonv2.Marshal(valid); err != nil {
			t.Fatalf("validated timestamp %v cannot be encoded: %v", createdAt, err)
		}
	}
}
