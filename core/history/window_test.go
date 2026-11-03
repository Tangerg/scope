package history_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
	"github.com/Tangerg/scope/core/metadata"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestNewWindowReaderValidatesConstruction(t *testing.T) {
	if _, err := history.NewWindowReader(nil, 1); !errors.Is(err, history.ErrNilReader) {
		t.Fatalf("nil reader error = %v", err)
	}
	var typedNil *inmemory.Store
	if _, err := history.NewWindowReader(typedNil, 1); !errors.Is(err, history.ErrNilReader) {
		t.Fatalf("typed-nil reader error = %v", err)
	}
	if _, err := history.NewWindowReader(new(inmemory.Store), 0); !errors.Is(err, history.ErrInvalidWindow) {
		t.Fatalf("invalid limit error = %v", err)
	}
}

func TestWindowReaderMergesSystemAndKeepsRecentMessages(t *testing.T) {
	base := new(inmemory.Store)
	firstSystem := chat.NewSystemMessage("first")
	firstSystem.Metadata = metadata.Map{}
	if err := firstSystem.Metadata.Set("shared", "first"); err != nil {
		t.Fatal(err)
	}
	secondSystem := chat.NewSystemMessage("second")
	secondSystem.Metadata = metadata.Map{}
	if err := secondSystem.Metadata.Set("shared", "second"); err != nil {
		t.Fatal(err)
	}
	messages := []chat.Message{firstSystem, chat.NewUserMessage(chat.NewTextPart("one")), secondSystem}
	for _, text := range []string{"two", "three", "four"} {
		messages = append(messages, chat.NewUserMessage(chat.NewTextPart(text)))
	}
	if _, err := appendMessages(t, base, "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowReader(base, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := window.Read(t.Context(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Role != chat.RoleSystem || got[0].Text() != "first\n\nsecond" || got[1].Text() != "three" || got[2].Text() != "four" {
		t.Fatalf("window = %#v", got)
	}
	if value := string(got[0].Metadata["shared"]); value != `"second"` {
		t.Fatalf("merged metadata = %s", value)
	}
}

func TestWindowReaderKeepsCompleteToolTurn(t *testing.T) {
	base := new(inmemory.Store)
	messages := []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("old question")),
		chat.NewAssistantMessage(chat.NewTextPart("old answer")),
		chat.NewUserMessage(chat.NewTextPart("new question")),
		chat.NewAssistantMessage(
			chat.NewReasoningPart("inspect first", []byte("signature")),
			chat.NewToolCallPart(chat.ToolCall{ID: "call-a", Name: "read", Arguments: `{}`}),
			chat.NewToolCallPart(chat.ToolCall{ID: "call-b", Name: "search", Arguments: `{}`}),
		),
		chat.NewToolMessage(
			chat.ToolResult{ID: "call-a", Name: "read", Output: toolcontract.NewTextOutput("a")},
			chat.ToolResult{ID: "call-b", Name: "search", Output: toolcontract.NewTextOutput("b")},
		),
		chat.NewAssistantMessage(chat.NewTextPart("new answer")),
	}
	if _, err := appendMessages(t, base, "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowReader(base, 4)
	if err != nil {
		t.Fatal(err)
	}
	got, err := window.Read(t.Context(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("len(window) = %d, want 4", len(got))
	}
	for i := range got {
		if got[i].Role != messages[i+2].Role || got[i].Text() != messages[i+2].Text() {
			t.Fatalf("window[%d] = %#v, want %#v", i, got[i], messages[i+2])
		}
	}
}

func TestWindowReaderRejectsSplitNewestTurn(t *testing.T) {
	base := new(inmemory.Store)
	messages := []chat.Message{
		chat.NewSystemMessage("system"),
		chat.NewUserMessage(chat.NewTextPart("question")),
		chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "read", Arguments: `{}`})),
		chat.NewToolMessage(chat.ToolResult{ID: "call", Name: "read", Output: toolcontract.NewTextOutput("result")}),
	}
	if _, err := appendMessages(t, base, "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowReader(base, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := window.Read(t.Context(), "c"); !errors.Is(err, history.ErrWindowTooSmall) {
		t.Fatalf("Read error = %v, want ErrWindowTooSmall", err)
	}
}

func TestWindowReaderPreservesStandaloneNewestUserTurn(t *testing.T) {
	base := new(inmemory.Store)
	messages := []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("old question")),
		chat.NewAssistantMessage(chat.NewTextPart("old answer")),
		chat.NewUserMessage(chat.NewTextPart("new question")),
	}
	if _, err := appendMessages(t, base, "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowReader(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := window.Read(t.Context(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Role != chat.RoleUser || got[0].Text() != "new question" {
		t.Fatalf("window = %#v", got)
	}
}

func TestWindowReaderPreservesSystemPartStructure(t *testing.T) {
	base := new(inmemory.Store)
	part := chat.NewTextPart("metadata-bearing instruction")
	part.Metadata = metadata.Map{}
	if err := part.Metadata.Set("provider", "preserved"); err != nil {
		t.Fatal(err)
	}
	first := chat.Message{Role: chat.RoleSystem, Parts: []chat.Part{chat.NewTextPart("first"), part}}
	second := chat.NewSystemMessage("second")
	if _, err := appendMessages(t, base, "c", first, second); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowReader(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := window.Read(t.Context(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Parts) != 4 {
		t.Fatalf("merged messages = %#v", got)
	}
	if value := string(got[0].Parts[1].Metadata["provider"]); value != `"preserved"` {
		t.Fatalf("part metadata = %s", value)
	}
	got[0].Parts[1].Metadata["provider"][1] = 'X'
	again, err := window.Read(t.Context(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if value := string(again[0].Parts[1].Metadata["provider"]); value != `"preserved"` {
		t.Fatalf("reading window exposed stored part metadata: %s", value)
	}
}

func TestWindowReaderExposesOnlyReadAuthority(t *testing.T) {
	base := new(inmemory.Store)
	window, err := history.NewWindowReader(base, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, writes := any(window).(history.Writer); writes {
		t.Fatal("read projection acquired write authority")
	}
	if _, clears := any(window).(history.Clearer); clears {
		t.Fatal("read projection acquired clear authority")
	}
	middleware, err := history.NewMiddleware(history.MiddlewareConfig{Reader: window, Writer: base, ConversationID: "window"})
	if err != nil {
		t.Fatal(err)
	}
	model := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return response(chat.NewAssistantMessage(chat.NewTextPart("answer"))), nil
	}))
	if _, err := model.Call(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("question")))); err != nil {
		t.Fatal(err)
	}
	if messages, err := window.Read(t.Context(), "window"); err != nil || len(messages) != 2 {
		t.Fatalf("explicit reader/writer composition lost the completed turn: %v %v", messages, err)
	}
}

func TestWindowReaderLargeLimitUsesOnlyActualHistory(t *testing.T) {
	base := new(inmemory.Store)
	window, err := history.NewWindowReader(base, int(^uint(0)>>1))
	if err != nil {
		t.Fatal(err)
	}
	for count := range 3 {
		got, err := window.Read(t.Context(), "conversation")
		if err != nil || got == nil || len(got) != count {
			t.Fatalf("Read after %d writes = %v, %v", count, got, err)
		}
		if _, err := appendMessages(t, base, "conversation", chat.NewUserMessage(chat.NewTextPart("turn"))); err != nil {
			t.Fatal(err)
		}
	}
}
