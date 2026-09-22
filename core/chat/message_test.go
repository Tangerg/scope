package chat_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func mustImage(t *testing.T) *media.Media {
	t.Helper()
	image, err := media.NewBytes("image/png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func validToolCall() chat.ToolCall {
	return chat.ToolCall{ID: "call-1", Name: "weather", Arguments: `{"city":"Paris"}`}
}

func validToolResult() chat.ToolResult {
	output, err := chat.NewJSONToolOutput(json.RawMessage(`{"temperature":20}`))
	if err != nil {
		panic(err)
	}
	return chat.ToolResult{ID: "call-1", Name: "weather", Output: output}
}

func TestMessageConstructors(t *testing.T) {
	part := chat.NewTextPart("hello")
	parts := []chat.Part{part}

	tests := []struct {
		name string
		got  chat.Message
		role chat.Role
		kind chat.PartKind
	}{
		{name: "system", got: chat.NewSystemMessage("rules"), role: chat.RoleSystem, kind: chat.PartText},
		{name: "user", got: chat.NewUserMessage(parts...), role: chat.RoleUser, kind: chat.PartText},
		{name: "assistant", got: chat.NewAssistantMessage(parts...), role: chat.RoleAssistant, kind: chat.PartText},
		{name: "tool", got: chat.NewToolMessage(validToolResult()), role: chat.RoleTool, kind: chat.PartToolResult},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got.Role != tt.role || len(tt.got.Parts) != 1 || tt.got.Parts[0].Kind != tt.kind {
				t.Fatalf("message = %+v, want role %q and kind %q", tt.got, tt.role, tt.kind)
			}
			if err := tt.got.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}

	parts[0] = chat.NewTextPart("changed")
	user := tests[1].got
	if user.Parts[0].Text != "hello" {
		t.Fatalf("NewUserMessage retained caller slice: %+v", user.Parts)
	}
}

func TestMessageValidateRolePartMatrix(t *testing.T) {
	image := mustImage(t)
	tests := []chat.Message{
		chat.NewSystemMessage("rules"),
		chat.NewUserMessage(chat.NewTextPart("question"), chat.NewMediaPart(image)),
		chat.NewAssistantMessage(
			chat.NewReasoningPart("thinking", []byte("signature")),
			chat.NewTextPart("answer"),
			chat.NewToolCallPart(validToolCall()),
			chat.NewRefusalPart("cannot continue"),
			chat.NewMediaPart(image),
		),
		chat.NewToolMessage(validToolResult()),
	}
	for _, message := range tests {
		if err := message.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", message, err)
		}
	}
}

func TestMessageValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		msg  chat.Message
	}{
		{name: "unknown role", msg: chat.Message{Role: "future", Parts: []chat.Part{chat.NewTextPart("x")}}},
		{name: "no parts", msg: chat.Message{Role: chat.RoleUser}},
		{name: "system media", msg: chat.Message{Role: chat.RoleSystem, Parts: []chat.Part{chat.NewMediaPart(mustImage(t))}}},
		{name: "user reasoning", msg: chat.Message{Role: chat.RoleUser, Parts: []chat.Part{chat.NewReasoningPart("why", nil)}}},
		{name: "assistant tool result", msg: chat.Message{Role: chat.RoleAssistant, Parts: []chat.Part{chat.NewToolResultPart(validToolResult())}}},
		{name: "tool text", msg: chat.Message{Role: chat.RoleTool, Parts: []chat.Part{chat.NewTextPart("result")}}},
		{name: "invalid nested part", msg: chat.Message{Role: chat.RoleUser, Parts: []chat.Part{{Kind: chat.PartText}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.msg.Validate(); !errors.Is(err, chat.ErrInvalidMessage) {
				t.Fatalf("Validate error = %v, want ErrInvalidMessage", err)
			}
		})
	}
}

func TestMessageValidateRecursesIntoMetadata(t *testing.T) {
	msg := chat.NewUserMessage(chat.NewTextPart("hello"))
	msg.Metadata = metadata.Map{"bad": json.RawMessage(`{`)}
	if err := msg.Validate(); !errors.Is(err, metadata.ErrInvalidValue) || !errors.Is(err, chat.ErrInvalidMessage) {
		t.Fatalf("Validate error = %v, want metadata and message errors", err)
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	image := mustImage(t)
	if err := image.Metadata.Set("source", "fixture"); err != nil {
		t.Fatal(err)
	}

	messages := []chat.Message{
		chat.NewSystemMessage("rules"),
		chat.NewUserMessage(chat.NewTextPart("question"), chat.NewMediaPart(image)),
		chat.NewAssistantMessage(
			chat.NewReasoningPart("thinking", []byte("signature")),
			chat.NewTextPart("answer"),
			chat.NewToolCallPart(validToolCall()),
		),
		chat.NewToolMessage(validToolResult()),
	}
	for i := range messages {
		messages[i].Metadata = metadata.Map{}
		if err := messages[i].Metadata.Set("index", i); err != nil {
			t.Fatal(err)
		}

		encoded, err := jsonv2.Marshal(messages[i])
		if err != nil {
			t.Fatalf("messages[%d] Marshal: %v", i, err)
		}
		if !strings.Contains(string(encoded), `"role":"`+string(messages[i].Role)+`"`) || !strings.Contains(string(encoded), `"kind":"`) {
			t.Fatalf("messages[%d] missing discriminators: %s", i, encoded)
		}

		var got chat.Message
		if err := jsonv2.Unmarshal(encoded, &got); err != nil {
			t.Fatalf("messages[%d] Unmarshal: %v", i, err)
		}
		if !reflect.DeepEqual(got, messages[i]) {
			t.Fatalf("messages[%d] round trip = %#v, want %#v", i, got, messages[i])
		}
	}
}

func TestMessageUnmarshalRejectsUnknownRoleWithoutMutatingReceiver(t *testing.T) {
	got := chat.NewSystemMessage("keep")
	err := jsonv2.Unmarshal([]byte(`{"role":"future","parts":[{"kind":"text","text":"replace"}]}`), &got)
	if !errors.Is(err, chat.ErrInvalidMessage) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidMessage", err)
	}
	if got.Role != chat.RoleSystem || got.Parts[0].Text != "keep" {
		t.Fatalf("failed Unmarshal mutated receiver: %+v", got)
	}
}

func TestMessageNilUnmarshalReceiver(t *testing.T) {
	var message *chat.Message
	if err := message.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidMessage) {
		t.Fatalf("UnmarshalJSON error = %v, want ErrInvalidMessage", err)
	}
}

func TestRoleValid(t *testing.T) {
	for _, role := range []chat.Role{chat.RoleSystem, chat.RoleUser, chat.RoleAssistant, chat.RoleTool} {
		if !role.Valid() {
			t.Errorf("%q must be valid", role)
		}
	}
	if chat.Role("future").Valid() {
		t.Fatal("future role must be invalid")
	}
}
