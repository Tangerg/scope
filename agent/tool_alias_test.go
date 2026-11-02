package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestModelAliasBindsOneExactToolIdentity(t *testing.T) {
	calls := 0
	makeTool := func(name string) tool.Tool {
		value, err := tool.NewFunc(tool.FuncConfig{Name: name}, func(context.Context, struct{}) (string, error) { calls++; return "ok", nil })
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	dispatcher := newToolDispatcher(nil)
	if err := dispatcher.bindTool(makeTool("report.write"), false); err != nil {
		t.Fatal(err)
	}
	definitions := dispatcher.manifest.initialDefinitions()
	if len(definitions) != 1 || definitions[0].Name != "report_write" {
		t.Fatalf("advertised tools: %v", definitions)
	}
	if err := dispatcher.bindTool(makeTool("report/write"), false); err == nil {
		t.Fatal("ambiguous alias admitted")
	}
	for _, name := range []string{"report.write", "unseen"} {
		prepared := dispatcher.prepareToolCall(chat.ToolCall{ID: "correlation", Name: name}, nil)
		if prepared.rejection == nil {
			t.Fatalf("unadvertised name %q accepted", name)
		}
	}
	prepared := dispatcher.prepareToolCall(chat.ToolCall{ID: "correlation", Name: "report_write", Arguments: "{}"}, nil)
	if prepared.rejection != nil || prepared.defect != nil {
		t.Fatalf("alias rejected: %+v", prepared)
	}
	if _, err := prepared.binding.Call(t.Context(), prepared.invocation); err != nil {
		t.Fatal(err)
	}
	other, err := tool.Bind(makeTool("report.write"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Call(t.Context(), prepared.invocation); !errors.Is(err, tool.ErrInvalidInvocation) {
		t.Fatalf("foreign binding accepted: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls: %d", calls)
	}
}
