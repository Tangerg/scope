package interaction_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// panicInputValidatorTool admits {} by schema but its input validator panics at
// Prepare time. The panic is an internal defect, not a caller argument error.
type panicInputValidatorTool struct{ called *atomic.Bool }

func (panicInputValidatorTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        "ask",
		Description: "A tool whose input validator panics.",
		InputSchema: []byte(`{"type":"object","additionalProperties":false}`),
	}
}

func (p panicInputValidatorTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	p.called.Store(true)
	return tool.Result{Output: chat.NewTextToolOutput("unreachable")}, nil
}

func (panicInputValidatorTool) InputValidator() func([]byte) error {
	return func([]byte) error { panic("internal-validator-marker") }
}

// A tool input-validator defect is an internal fault, not a model-correctable
// argument error. It must settle the Tool call as a definite failure that
// terminates the interaction, never feed the recovered value back to the model.
func TestToolInputValidatorPanicSettlesDefinitelyWithoutLeaking(t *testing.T) {
	var called atomic.Bool
	var mu sync.Mutex
	var seenByModel strings.Builder
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		mu.Lock()
		for _, message := range request.Messages {
			seenByModel.WriteString(message.Text())
		}
		mu.Unlock()
		return toolCallResponse(chat.ToolCall{ID: "call", Name: "ask", Arguments: `{}`}), nil
	})
	deployment := newDeployment(t, model, []tool.Tool{panicInputValidatorTool{called: &called}}, 3)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})

	result, err := engine.Run(context.Background(), deployment.Deployment, interactionInput(t, "go"))
	if err != nil {
		t.Fatal(err)
	}

	failure, present := result.Termination().Failure()
	if !present {
		t.Fatalf("termination = %#v, want a definite failure", result.Termination())
	}
	if called.Load() {
		t.Fatal("Tool.Call ran despite an input-validator defect")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(seenByModel.String(), "internal-validator-marker") {
		t.Fatal("validator panic value reached the model context")
	}
	if strings.Contains(failure.Message(), "internal-validator-marker") {
		t.Fatal("validator panic value leaked into the failure diagnostic")
	}
}
