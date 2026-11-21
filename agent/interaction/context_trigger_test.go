package interaction

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// triggeredPreparer prepares only the model calls its trigger selects.
type triggeredPreparer struct {
	trigger func() func(uint64, *chat.Request) bool
	prepare contextPreparerFunc
}

func (t triggeredPreparer) PreparationTrigger() func(uint64, *chat.Request) bool { return t.trigger() }

func (t triggeredPreparer) PrepareContext(ctx context.Context, invocation ContextInvocation, request *chat.Request) ([]chat.Message, error) {
	return t.prepare(ctx, invocation, request)
}

// echoRoundModel calls the echo Tool once and then answers.
func echoRoundModel() chat.Model {
	calls := 0
	return chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		if calls == 1 {
			return callResponse(requested("echo", "first")), nil
		}
		return finalResponse("done"), nil
	})
}

func echoTools(t *testing.T) ToolSet {
	return toolSetTest(t, scriptTool{name: "echo", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput("echoed")}, nil
	}})
}

func TestPreparationTriggerSelectsWhichModelCallsArePrepared(t *testing.T) {
	type observation struct {
		sequence uint64
		tools    []string
	}
	var observed []observation
	var prepared []uint64
	preparer := triggeredPreparer{
		trigger: func() func(uint64, *chat.Request) bool {
			return func(sequence uint64, request *chat.Request) bool {
				names := make([]string, len(request.Tools))
				for index, definition := range request.Tools {
					names[index] = definition.Name
				}
				observed = append(observed, observation{sequence: sequence, tools: names})
				return sequence == 2
			}
		},
		prepare: func(_ context.Context, invocation ContextInvocation, request *chat.Request) ([]chat.Message, error) {
			prepared = append(prepared, invocation.Sequence)
			return request.Messages, nil
		},
	}
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	program := compileTest(t, runtime, Config{Revision: "trigger", Tools: echoTools(t), ContextPreparer: preparer, Model: CallModel(echoRoundModel())})
	handle := startInteraction(t, runtime, program)
	if output := awaitOutput(t, handle); output.ModelResponse.Text() != "done" {
		t.Fatalf("output = %+v", output)
	}
	if !slices.EqualFunc(observed, []observation{{1, []string{"echo"}}, {2, []string{"echo"}}}, func(a, b observation) bool {
		return a.sequence == b.sequence && slices.Equal(a.tools, b.tools)
	}) {
		t.Fatalf("trigger observed %+v, want each model call's sequence and request", observed)
	}
	var operations []operation
	for _, intent := range loggedIntents(completedLog(t, store, handle.ID())) {
		operations = append(operations, mustValue(decodeEffect(intent.Effect.Payload())).operation())
	}
	if want := []operation{operationModelCall, operationToolCall, operationPrepare, operationModelCall}; !slices.Equal(operations, want) || !slices.Equal(prepared, []uint64{2}) {
		t.Fatalf("operations=%v prepared=%v, want only the selected call prepared", operations, prepared)
	}
}

func TestCompileRejectsAnInvalidPreparationTrigger(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	model := CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return finalResponse("unused"), nil }))
	for name, trigger := range map[string]func() func(uint64, *chat.Request) bool{
		"nil trigger":    func() func(uint64, *chat.Request) bool { return nil },
		"panicking read": func() func(uint64, *chat.Request) bool { panic("trigger unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			preparer := triggeredPreparer{trigger: trigger, prepare: unusedPreparer}
			_, err := Compile(runtime.Compiler(), Config{Name: "test.trigger", Description: "Reject an unusable trigger.", Revision: "trigger", Model: model, ContextPreparer: preparer})
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Compile admitted %s: %v", name, err)
			}
		})
	}
}

func TestContextPolicyReadsTheRequestTheModelCallWouldSend(t *testing.T) {
	var seen [][]string
	policy := func(request *chat.Request) ([]chat.Message, error) {
		names := make([]string, len(request.Tools))
		for index, definition := range request.Tools {
			names[index] = definition.Name
		}
		seen = append(seen, names)
		return request.Messages, nil
	}
	runtime := conformancetest.Runtime(t, run.Config{})
	program := compileTest(t, runtime, Config{Revision: "policy", Tools: echoTools(t), ContextPolicy: policy, Model: CallModel(echoRoundModel())})
	awaitOutput(t, startInteraction(t, runtime, program))
	if len(seen) != 2 || !slices.Equal(seen[0], []string{"echo"}) || !slices.Equal(seen[1], []string{"echo"}) {
		t.Fatalf("ContextPolicy saw Tools %v, want the advertised definitions before each call", seen)
	}
}
