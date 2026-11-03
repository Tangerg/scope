package interaction

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolSchedulingUsesOnlyTheNextExclusiveBoundary(t *testing.T) {
	for _, row := range []struct {
		name     string
		classify func(tool.Invocation) (string, bool)
		calls    []chat.ToolCall
		count    int
	}{
		{"undeclared capability", nil, []chat.ToolCall{requested("echo", "a"), requested("echo", "b")}, 1},
		{"exclusive declaration", func(tool.Invocation) (string, bool) { return "", false }, []chat.ToolCall{requested("echo", "a"), requested("echo", "b")}, 1},
		{"same resource", func(tool.Invocation) (string, bool) { return "shared", true }, []chat.ToolCall{requested("echo", "a"), requested("echo", "b")}, 1},
		{"independent resources", func(tool.Invocation) (string, bool) { return "", true }, []chat.ToolCall{requested("echo", "a"), requested("echo", "b")}, 2},
		{"unknown next binding", func(tool.Invocation) (string, bool) { return "", true }, []chat.ToolCall{requested("echo", "a"), requested("missing", "b")}, 1},
		{"invalid arguments do not authorize overlap", func(tool.Invocation) (string, bool) { return "", true }, []chat.ToolCall{requested("echo", "a"), {Name: "echo", ID: "b", Arguments: `{"invalid":true}`}}, 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			manifest := toolSetTest(t, scriptTool{name: "echo", classify: row.classify}).manifest()
			count, err := manifest.concurrentBatchEnd(t.Context(), row.calls)
			if err != nil || count != row.count {
				t.Fatalf("batch size=%d, error=%v", count, err)
			}
		})
	}
	var inspections int
	manifest := toolSetTest(t, scriptTool{name: "echo", classify: func(tool.Invocation) (string, bool) { inspections++; return "shared", true }}).manifest()
	calls := make([]chat.ToolCall, 100)
	for index := range calls {
		calls[index] = requested("echo", strconv.Itoa(index))
	}
	for len(calls) != 0 {
		count := mustValue(manifest.concurrentBatchEnd(t.Context(), calls))
		calls = calls[count:]
	}
	if inspections != 199 {
		t.Fatalf("exclusive scheduling inspected beyond its boundary: %d", inspections)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := manifest.concurrentBatchEnd(ctx, []chat.ToolCall{requested("echo", "a")}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled classification continued", err)
	}
	panicking := toolSetTest(t, scriptTool{name: "echo", classify: func(tool.Invocation) (string, bool) { panic("classifier defect") }}).manifest()
	if _, err := panicking.concurrentBatchEnd(t.Context(), []chat.ToolCall{requested("echo", "a")}); err == nil || !strings.Contains(err.Error(), "classifier defect") {
		t.Fatal("classifier panic did not remain an explicit failure", err)
	}
}

func TestConcurrentToolCompletionRetainsTheProviderOrder(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	started := make(chan tool.Invocation, 2)
	executable := scriptTool{name: "echo", direct: true, classify: func(tool.Invocation) (string, bool) { return "", true }, invoke: func(ctx context.Context, input tool.Invocation) (tool.Result, error) {
		started <- input
		if requestedCall(input) == "first" {
			select {
			case <-release:
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
		}
		return tool.Result{Output: tool.NewTextOutput(requestedCall(input))}, nil
	}}
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, executable), MaxConcurrentToolCalls: 2, Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("echo", "first"), requested("echo", "second")), nil
	}))})
	handle := startInteraction(t, runtime, program)
	ctx, cancel := context.WithTimeout(t.Context(), interactionTestTimeout)
	defer cancel()
	var first, second tool.IdempotencyKey
	for range 2 {
		select {
		case input := <-started:
			if requestedCall(input) == "first" {
				first = input.IdempotencyKey()
			} else {
				second = input.IdempotencyKey()
			}
		case <-ctx.Done():
			t.Fatal("independent tool calls did not overlap", ctx.Err())
		}
	}
	log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		for _, entry := range log.Entries {
			if settled, ok := entry.Record.(run.EffectSettled); ok && idempotencyKey(settled.ID) == second {
				return true
			}
		}
		return false
	})
	rounds := mustValue(SettledResults(program, log))
	if len(rounds) != 1 || rounds[0].Complete() || len(rounds[0].Entries()) != 1 || rounds[0].Entries()[0].ToolCallIndex != 1 || idempotencyKey(rounds[0].Entries()[0].Reference) != second {
		t.Fatal("an early second result changed the provider's proposal indexes")
	}
	if reference, found := rounds[0].Reference(0); !found || idempotencyKey(reference) != first {
		t.Fatal("an unfinished sibling lost its committed effect identity")
	}
	releaseOnce.Do(func() { close(release) })
	output := awaitOutput(t, handle)
	if len(output.DirectToolResults) != 2 || output.DirectToolResults[0].ID != "first" || output.DirectToolResults[1].ID != "second" {
		t.Fatal("completion order changed the final proposal order")
	}
}

type inputValidatorTool struct {
	called *atomic.Bool
}

func (i inputValidatorTool) Definition() tool.Definition {
	return tool.Definition{Name: "checked", Description: "Validate inputs before effects.", InputSchema: []byte(objectToolSchema)}
}
func (i inputValidatorTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	i.called.Store(true)
	return tool.Result{Output: tool.NewTextOutput("must not execute")}, nil
}
func (i inputValidatorTool) InputValidator() func([]byte) error {
	return func([]byte) error { panic("private-validator-detail") }
}
func (i inputValidatorTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) {
	return func(tool.Invocation) (string, bool) { return "", true }
}

func TestInputValidatorDefectCannotExecuteOrLeakToModelFeedback(t *testing.T) {
	var called atomic.Bool
	tools := toolSetTest(t, inputValidatorTool{called: &called})
	manifest := tools.manifest()
	if _, err := manifest.concurrentBatchEnd(t.Context(), []chat.ToolCall{requested("checked", "call")}); !errors.Is(err, errToolInputValidatorDefect) {
		t.Fatal("validator defect became a scheduling decision", err)
	}
	runtime := conformancetest.Runtime(t, run.Config{})
	program := compileTest(t, runtime, Config{Tools: tools, Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("checked", "call")), nil
	}))})
	result, err := startInteraction(t, runtime, program).Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	failure, ok := result.Termination().Failure()
	if !ok || called.Load() || strings.Contains(failure.Message(), "private-validator-detail") {
		t.Fatalf("validator defect lost its boundary: %+v called=%v", failure, called.Load())
	}
	prepared := tools.dispatcher.prepareToolCall(requested("checked", "call"), tool.Admission{})
	if prepared.defect == nil || prepared.rejection != nil {
		t.Fatal("dispatch validator defect became model-correctable feedback")
	}
}

func TestToolDefiniteFailuresRemainModelFeedbackAndNeverAdvertise(t *testing.T) {
	for _, kind := range []tool.FailureKind{tool.FailureKindFailed, tool.FailureKindRejected} {
		runtime := conformancetest.Runtime(t, run.Config{})
		calls, models := 0, 0
		tools := toolSetTest(t, scriptTool{name: "write", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
			calls++
			return tool.Result{}, mustValue(tool.NewFailure(tool.FailureConfig{Kind: kind, Cause: errors.New("private cause"), Output: tool.NewTextOutput("public refusal")}))
		}, advertise: func(context.Context, tool.Invocation, tool.Result) ([]string, error) {
			t.Error("unsuccessful tool was given advertisement authority")
			return nil, nil
		}})
		program := compileTest(t, runtime, Config{Tools: tools, Model: CallModel(chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
			models++
			if models == 1 {
				return callResponse(requested("write", "call")), nil
			}
			parts := request.Messages[len(request.Messages)-1].Parts
			if len(parts) != 1 || parts[0].ToolResult == nil || !parts[0].ToolResult.IsError {
				return nil, errors.New("definite failure lost model feedback")
			}
			text, _ := parts[0].ToolResult.Output.Text()
			if text != "public refusal" {
				return nil, errors.New("diagnostic cause replaced public refusal")
			}
			return finalResponse("handled"), nil
		}))})
		if output := awaitOutput(t, startInteraction(t, runtime, program)); output.ModelResponse.Text() != "handled" || calls != 1 || models != 2 {
			t.Fatal("definite tool failure changed replay or completion semantics")
		}
	}
}

func TestToolPanicRequiresReconciliationOfTheSameDirectEffect(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	calls := 0
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, scriptTool{name: "panic", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		calls++
		panic("write acknowledgement interrupted")
	}}), Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("panic", "call")), nil
	}))})
	handle := startInteraction(t, runtime, program)
	var pending run.PendingEffect
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		inspection := mustValue(log.Inspect())
		process, _ := inspection.Process(handle.ID())
		for _, candidate := range process.PendingEffects {
			if candidate.Outcome.Status() == run.SettlementStatusUnknown {
				pending = candidate
				return true
			}
		}
		return false
	})
	projection := mustValue(ToolSettlementFromOutcome(requested("panic", "call"), pending.Outcome))
	if projection.Result != nil || projection.Outcome != ToolOutcomeUnknown || projection.Failure == "" || pending.Failure != nil {
		t.Fatal("panic established an outcome or lost typed tool evidence")
	}
	settlement := mustValue(SettleToolResult(program, pending.Request, tool.Result{Output: tool.NewTextOutput("investigated write")}, ResultSucceeded, nil))
	if err := handle.ResolveUnknownEffect(t.Context(), pending.Request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	output := awaitOutput(t, handle)
	if len(output.DirectToolResults) != 1 || output.DirectToolResults[0].ID != "call" || calls != 1 {
		t.Fatal("reconciliation repeated or reassigned the tool call")
	}
}

func TestReplayPolicyBelongsToEachTool(t *testing.T) {
	tools := toolSetTest(t, scriptTool{name: "read", idempotent: true}, scriptTool{name: "write"})
	definition := mustValue(newDefinition(definitionConfig{Name: "test.replay", Description: "Mix replayable and unreplayable tools.", Tools: tools}))
	dispatcher := mustValue(newDispatcher(definition, dispatcherConfig{Tools: tools, Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return nil, errors.New("must not call the model")
	}))}))
	for name, want := range map[string]run.ReplayPolicy{"read": run.ReplayPolicySameIdentity, "write": run.ReplayPolicyNever} {
		effect := mustValue(toolEffect(toolDispatchRequest{Invocation: toolCall{Call: requested(name, name)}}))
		if got := dispatcher.Policy(effect).Replay; got != want {
			t.Errorf("%s replay = %s; want %s", name, got, want)
		}
	}
}
