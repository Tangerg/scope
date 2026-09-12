package history_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
	"github.com/Tangerg/scope/core/metadata"
)

func TestNewWindowStoreValidatesConstruction(t *testing.T) {
	if _, err := history.NewWindowStore(nil, 1); !errors.Is(err, history.ErrNilStore) {
		t.Fatalf("nil store error = %v", err)
	}
	var typedNil *inmemory.Store
	if _, err := history.NewWindowStore(typedNil, 1); !errors.Is(err, history.ErrNilStore) {
		t.Fatalf("typed-nil store error = %v", err)
	}
	if _, err := history.NewWindowStore(new(inmemory.Store), 0); !errors.Is(err, history.ErrInvalidWindow) {
		t.Fatalf("invalid limit error = %v", err)
	}
}

func TestWindowStoreMergesSystemAndKeepsRecentMessages(t *testing.T) {
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
	if _, err := base.Write(t.Context(), "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowStore(base, 3)
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

func TestWindowStoreKeepsCompleteToolTurn(t *testing.T) {
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
			chat.ToolResult{ID: "call-a", Name: "read", Output: chat.NewTextToolOutput("a")},
			chat.ToolResult{ID: "call-b", Name: "search", Output: chat.NewTextToolOutput("b")},
		),
		chat.NewAssistantMessage(chat.NewTextPart("new answer")),
	}
	if _, err := base.Write(t.Context(), "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowStore(base, 4)
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

func TestWindowStoreRejectsSplitNewestTurn(t *testing.T) {
	base := new(inmemory.Store)
	messages := []chat.Message{
		chat.NewSystemMessage("system"),
		chat.NewUserMessage(chat.NewTextPart("question")),
		chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "read", Arguments: `{}`})),
		chat.NewToolMessage(chat.ToolResult{ID: "call", Name: "read", Output: chat.NewTextToolOutput("result")}),
	}
	if _, err := base.Write(t.Context(), "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowStore(base, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := window.Read(t.Context(), "c"); !errors.Is(err, history.ErrWindowTooSmall) {
		t.Fatalf("Read error = %v, want ErrWindowTooSmall", err)
	}
}

func TestWindowStorePreservesStandaloneNewestUserTurn(t *testing.T) {
	base := new(inmemory.Store)
	messages := []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("old question")),
		chat.NewAssistantMessage(chat.NewTextPart("old answer")),
		chat.NewUserMessage(chat.NewTextPart("new question")),
	}
	if _, err := base.Write(t.Context(), "c", messages...); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowStore(base, 1)
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

func TestWindowStorePreservesSystemPartStructure(t *testing.T) {
	base := new(inmemory.Store)
	part := chat.NewTextPart("metadata-bearing instruction")
	part.Metadata = metadata.Map{}
	if err := part.Metadata.Set("provider", "preserved"); err != nil {
		t.Fatal(err)
	}
	first := chat.Message{Role: chat.RoleSystem, Parts: []chat.Part{chat.NewTextPart("first"), part}}
	second := chat.NewSystemMessage("second")
	if _, err := base.Write(t.Context(), "c", first, second); err != nil {
		t.Fatal(err)
	}
	window, err := history.NewWindowStore(base, 1)
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

func TestWindowStoreDelegatesWritesAndClear(t *testing.T) {
	base := new(inmemory.Store)
	window, err := history.NewWindowStore(base, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := window.Write(t.Context(), "b", chat.NewUserMessage(chat.NewTextPart("one"))); err != nil {
		t.Fatal(err)
	}
	if _, err := window.Write(t.Context(), "a", chat.NewUserMessage(chat.NewTextPart("one"))); err != nil {
		t.Fatal(err)
	}
	if err := window.Clear(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := window.Read(t.Context(), "a"); got == nil || len(got) != 0 {
		t.Fatalf("after Clear = %#v", got)
	}
}
