package chat_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestProtocolEncodingRejectsInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff})
	for name, value := range map[string]any{
		"text":            chat.NewTextPart(invalid),
		"reasoning":       chat.NewReasoningPart(invalid, nil),
		"refusal":         chat.NewRefusalPart(invalid),
		"message":         chat.NewUserMessage(chat.NewTextPart(invalid)),
		"tool output":     chat.NewTextToolOutput(invalid),
		"tool arguments":  chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "tool", Arguments: invalid}),
		"media reference": &media.Media{MIME: "image/png", Source: media.Source{Kind: media.SourceReference, Ref: invalid}},
		"metadata key":    metadata.Map{invalid: json.RawMessage(`true`)},
		"metadata value":  metadata.Map{"provider/state": json.RawMessage{'"', 0xff, '"'}},
		"model name":      chat.Options{Model: invalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(value); err == nil {
				t.Fatal("protocol codec silently repaired invalid UTF-8")
			}
		})
	}
}
