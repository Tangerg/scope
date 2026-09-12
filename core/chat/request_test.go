package chat_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func validToolDefinition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        "weather",
		Description: "look up weather",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
	}
}

func TestNewRequest(t *testing.T) {
	messages := []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}
	request, err := chat.NewRequest(messages...)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	messages[0] = chat.NewSystemMessage("changed")
	if request.Messages[0].Role != chat.RoleUser {
		t.Fatal("NewRequest retained the caller's message slice")
	}
	if !reflect.DeepEqual(request.Options, chat.Options{}) {
		t.Fatalf("Options = %+v, want legal zero value", request.Options)
	}
}

func TestRequestClone(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	if setErr := image.Metadata.Set("origin", "caller"); setErr != nil {
		t.Fatal(setErr)
	}
	user := chat.NewUserMessage(chat.NewMediaPart(image))
	if setErr := user.Metadata.Set("turn", 1); setErr != nil {
		t.Fatal(setErr)
	}
	assistant := chat.NewAssistantMessage(
		chat.NewReasoningPart("thinking", []byte("signature")),
		chat.NewToolCallPart(validToolCall()),
	)
	tool := chat.NewToolMessage(validToolResult())

	request := &chat.Request{
		Messages: []chat.Message{user, assistant, tool},
		Tools:    []chat.ToolDefinition{validToolDefinition()},
		Options: chat.Options{
			MaxOutputTokens: new(int64(10)),
			Stop:            []string{"END"},
			Temperature:     new(0.5),
		},
	}
	if setErr := request.Options.Extensions.Set("test/value", "caller"); setErr != nil {
		t.Fatal(setErr)
	}

	clone := request.Clone()
	clone.Messages[0].Metadata["turn"][0] = '9'
	clone.Messages[0].Parts[0].Media.Source.Bytes[0] = 'X'
	clone.Messages[0].Parts[0].Media.Metadata["origin"][1] = 'X'
	clone.Messages[1].Parts[0].ReasoningState[0] = 'X'
	clone.Messages[1].Parts[1].ToolCall.Name = "mutated"
	clone.Messages[2].Parts[0].ToolResult.Output.Details[0] = '['
	clone.Tools[0].InputSchema[0] = '['
	*clone.Options.MaxOutputTokens = 20
	clone.Options.Stop[0] = "MUTATED"
	*clone.Options.Temperature = 1
	if setErr := clone.Options.Extensions.Set("test/value", "changed"); setErr != nil {
		t.Fatal(setErr)
	}
	extension, found, err := request.Options.Extensions.Decode[string]("test/value")
	if err != nil || !found {
		t.Fatal(err)
	}

	if string(request.Messages[0].Metadata["turn"]) != "1" ||
		string(request.Messages[0].Parts[0].Media.Source.Bytes) != "image" ||
		string(request.Messages[0].Parts[0].Media.Metadata["origin"]) != `"caller"` ||
		string(request.Messages[1].Parts[0].ReasoningState) != "signature" ||
		request.Messages[1].Parts[1].ToolCall.Name != "weather" ||
		string(request.Messages[2].Parts[0].ToolResult.Output.Details) != `{"temperature":20}` ||
		request.Tools[0].InputSchema[0] != '{' ||
		*request.Options.MaxOutputTokens != 10 ||
		request.Options.Stop[0] != "END" ||
		*request.Options.Temperature != 0.5 || extension != "caller" {
		t.Fatalf("clone mutated source request: %#v", request)
	}

	var nilRequest *chat.Request
	if nilRequest.Clone() != nil {
		t.Fatal("nil Request.Clone must return nil")
	}
}

func TestRequestValidate(t *testing.T) {
	request, err := chat.NewRequest(chat.NewSystemMessage("rules"), chat.NewUserMessage(chat.NewTextPart("hello")))
	if err != nil {
		t.Fatal(err)
	}
	request.Tools = []chat.ToolDefinition{validToolDefinition()}
	format, err := chat.NewOutputFormat(chat.OutputFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	request.Options = chat.Options{Model: "model", OutputFormat: &format, Temperature: new(0.5)}
	if err := request.Options.Extensions.Set("openai/request", map[string]any{"seed": 42}); err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRequestValidateRejectsInvalidValues(t *testing.T) {
	validMessage := chat.NewUserMessage(chat.NewTextPart("hello"))
	invalidMessage := chat.Message{Role: chat.RoleUser}
	invalidOption := chat.Options{Temperature: new(3.0)}
	definition := validToolDefinition()

	tests := []struct {
		name    string
		request *chat.Request
		also    error
	}{
		{name: "nil", request: nil},
		{name: "no messages", request: &chat.Request{}},
		{name: "invalid message", request: &chat.Request{Messages: []chat.Message{invalidMessage}}, also: chat.ErrInvalidMessage},
		{name: "invalid tool", request: &chat.Request{Messages: []chat.Message{validMessage}, Tools: []chat.ToolDefinition{{}}}, also: chat.ErrInvalidToolDefinition},
		{name: "duplicate tool", request: &chat.Request{Messages: []chat.Message{validMessage}, Tools: []chat.ToolDefinition{definition, definition}}},
		{name: "invalid options", request: &chat.Request{Messages: []chat.Message{validMessage}, Options: invalidOption}, also: chat.ErrInvalidOptions},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()
			if !errors.Is(err, chat.ErrInvalidRequest) {
				t.Fatalf("Validate error = %v, want ErrInvalidRequest", err)
			}
			if tt.also != nil && !errors.Is(err, tt.also) {
				t.Fatalf("Validate error = %v, also want %v", err, tt.also)
			}
		})
	}
}

func TestRequestOptionsExtension(t *testing.T) {
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
	if err := request.Options.Extensions.Set("openai/response_format", map[string]string{"type": "json_object"}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	value, ok, err := request.Options.Extensions.Decode[map[string]string]("openai/response_format")
	if err != nil || !ok || value["type"] != "json_object" {
		t.Fatalf("Decode extension = (%v, %v, %v)", value, ok, err)
	}

	before := request.Options.Extensions.Clone()
	if err := request.Options.Extensions.Set("not-namespaced", 1); err == nil {
		t.Fatalf("unscoped key error = %v", err)
	}
	if err := request.Options.Extensions.Set("openai/bad", func() {}); err == nil {
		t.Fatalf("unsupported value error = %v", err)
	}
	if !request.Options.Extensions.Equal(before) {
		t.Fatalf("failed SetExtension mutated map: %#v, want %#v", request.Options.Extensions, before)
	}
}

func TestRequestJSONRoundTrip(t *testing.T) {
	request, _ := chat.NewRequest(
		chat.NewSystemMessage("rules"),
		chat.NewAssistantMessage(chat.NewToolCallPart(validToolCall())),
	)
	request.Tools = []chat.ToolDefinition{validToolDefinition()}
	request.Options = chat.Options{Model: "model", MaxOutputTokens: new(int64(100))}
	if err := request.Options.Extensions.Set("anthropic/cache_control", map[string]bool{"enabled": true}); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"extensions"`) || !strings.Contains(string(encoded), `"input_schema"`) {
		t.Fatalf("request JSON missing protocol fields: %s", encoded)
	}
	var got chat.Request
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, *request) {
		t.Fatalf("round trip = %#v, want %#v", got, *request)
	}
}

func TestRequestOmitsZeroOptions(t *testing.T) {
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"options"`) {
		t.Fatalf("zero Options must be omitted: %s", encoded)
	}
}

func TestRequestUnmarshalIsAtomic(t *testing.T) {
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("keep")))
	err := json.Unmarshal([]byte(`{"messages":[]}`), request)
	if !errors.Is(err, chat.ErrInvalidRequest) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidRequest", err)
	}
	if request.Messages[0].Parts[0].Text != "keep" {
		t.Fatalf("failed Unmarshal mutated request: %+v", request)
	}
}

func TestRequestNilUnmarshalReceiver(t *testing.T) {
	var request *chat.Request
	if err := request.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidRequest) {
		t.Fatalf("UnmarshalJSON error = %v, want ErrInvalidRequest", err)
	}
}

func TestRequestRequiresLeadingSystemPrefix(t *testing.T) {
	request := &chat.Request{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("hello")),
		chat.NewSystemMessage("late instruction"),
	}}
	if err := request.Validate(); !errors.Is(err, chat.ErrInvalidRequest) || !strings.Contains(err.Error(), "leading prefix") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestRequestValidatesToolChoiceAgainstDefinitions(t *testing.T) {
	message := chat.NewUserMessage(chat.NewTextPart("hello"))
	request := &chat.Request{
		Messages:   []chat.Message{message},
		ToolChoice: &chat.ToolChoice{Mode: chat.ToolChoiceAuto},
	}
	if err := request.Validate(); !errors.Is(err, chat.ErrInvalidRequest) {
		t.Fatalf("tool choice without tools error = %v", err)
	}

	request.Tools = []chat.ToolDefinition{validToolDefinition()}
	request.ToolChoice = &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "lookup"}
	if err := request.Validate(); !errors.Is(err, chat.ErrInvalidRequest) || !strings.Contains(err.Error(), "undefined tool") {
		t.Fatalf("undefined named choice error = %v", err)
	}

	request.ToolChoice.Name = "weather"
	if err := request.Validate(); err != nil {
		t.Fatalf("defined named choice: %v", err)
	}
}

func TestRequestProtocolFieldsContainNoInterfaces(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[chat.Request](),
		reflect.TypeFor[chat.Options](),
		reflect.TypeFor[chat.ToolDefinition](),
	} {
		for field := range typ.Fields() {
			if field.Type.Kind() == reflect.Interface {
				t.Errorf("%s.%s is an interface field", typ, field.Name)
			}
		}
	}
}

func TestRequestStructureAllowsClippedToolHistory(t *testing.T) {
	result := chat.ToolResult{ID: "call", Name: "tool", Output: chat.NewTextToolOutput("answer")}
	request, err := chat.NewRequest(chat.NewToolMessage(result))
	if err != nil {
		t.Fatal(err)
	}
	result.Output.Content[0].Text = "reused"
	if request.Messages[0].Parts[0].ToolResult.Output.Content[0].Text != "answer" {
		t.Fatal("request borrowed retained tool content")
	}
}
