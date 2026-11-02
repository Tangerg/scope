package interaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestTruncatedProposalIsRejectedAsOneAtomicRound(t *testing.T) {
	tools := toolSetTest(t, scriptTool{name: "write", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		t.Error("a truncated proposal executed external work")
		return tool.Result{}, errors.New("must not execute")
	}})
	definition := mustValue(newDefinition(definitionConfig{Name: "test.truncated", Description: "Reject incomplete proposals atomically.", Tools: tools}))
	models := 0
	dispatcher := mustValue(newDispatcher(definition, dispatcherConfig{Tools: tools, Model: chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			response := callResponse(requested("write", "first"), requested("write", "second"))
			response.Output.FinishReason = chat.FinishReasonLength
			return response, nil
		}
		parts := request.Messages[len(request.Messages)-1].Parts
		if len(parts) != 2 || parts[0].ToolResult == nil || parts[1].ToolResult == nil || !parts[0].ToolResult.IsError || !parts[1].ToolResult.IsError || parts[0].ToolResult.ID != "first" || parts[1].ToolResult.ID != "second" {
			return nil, errors.New("truncated proposal lost an ordered rejection")
		}
		return finalResponse("replanned"), nil
	})}))
	result := conformancetest.Run(t, run.BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "truncated-proposal"}, run.Config{}, mustValue(run.EncodePayload(testInput())))
	payload := mustValue(result.Output())
	if output := mustValue(payload.Decode[Output]()); output.ModelResponse.Text() != "replanned" || models != 2 {
		t.Fatal("truncated proposal did not resume through model feedback")
	}
}

func TestUnfinishedOrAmbiguousModelCallsCannotExecuteTools(t *testing.T) {
	for _, row := range []struct {
		name     string
		response *chat.Response
		code     string
	}{
		{"stop with calls", func() *chat.Response {
			response := callResponse(requested("write", "call"))
			response.Output.FinishReason = chat.FinishReasonStop
			return response
		}(), failureCodeInteractionModelToolCallsNotCompleted},
		{"duplicate call identity", callResponse(requested("write", "same"), requested("write", "same")), "interaction.model.invalid_response"},
		{"no finished assistant message", &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}, "interaction.model.invalid_response"},
	} {
		t.Run(row.name, func(t *testing.T) {
			runtime := conformancetest.Runtime(t, run.Config{})
			tools := toolSetTest(t, scriptTool{name: "write", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
				t.Error("invalid model proposal executed a tool")
				return tool.Result{}, nil
			}})
			program := compileTest(t, runtime, Config{Tools: tools, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return row.response, nil })})
			result, err := startInteraction(t, runtime, program).Await(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			failure, ok := result.Termination().Failure()
			if !ok || failure.Code() != row.code || failure.Kind() != run.FailureKindExternal {
				t.Fatalf("termination=%+v", result.Termination())
			}
		})
	}
}

func TestDefiniteModelFailureTerminatesWithoutAnotherModelCall(t *testing.T) {
	for _, kind := range []chat.CallFailureKind{chat.CallFailureFailed, chat.CallFailureNotExecuted} {
		runtime := conformancetest.Runtime(t, run.Config{})
		calls := 0
		program := compileTest(t, runtime, Config{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			calls++
			return nil, &chat.CallFailure{Kind: kind, Cause: errors.New("definite provider rejection")}
		})})
		result, err := startInteraction(t, runtime, program).Await(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		failure, ok := result.Termination().Failure()
		if !ok || failure.Code() != failureCodeInteractionHostFailed || calls != 1 {
			t.Fatalf("model failure=%+v calls=%d", failure, calls)
		}
	}
}

func TestUnadmittedToolArgumentsRejectTheWholeModelProposal(t *testing.T) {
	for _, invalid := range []chat.ToolCall{
		{ID: "wrong-shape", Name: "write", Arguments: `{"unexpected":true}`},
		{ID: "broken-json", Name: "write", Arguments: `{"partial":`},
	} {
		runtime := conformancetest.Runtime(t, run.Config{})
		models := 0
		program := compileTest(t, runtime, Config{Tools: toolSetTest(t, scriptTool{name: "write", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
			t.Error("valid prefix ran before admission of its sibling")
			return tool.Result{}, nil
		}}), Model: chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
			models++
			if models == 1 {
				return callResponse(requested("write", "prefix"), invalid), nil
			}
			for _, part := range request.Messages[len(request.Messages)-1].Parts {
				if part.ToolResult == nil || !part.ToolResult.IsError {
					return nil, errors.New("proposal admission dropped a rejection")
				}
			}
			return finalResponse("rejected"), nil
		})})
		if output := awaitOutput(t, startInteraction(t, runtime, program)); output.ModelCalls != 2 || output.ModelResponse.Text() != "rejected" {
			t.Fatal("invalid arguments did not produce bounded model correction")
		}
	}
}

func TestCompletionCancellationDoesNotCommitACandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	definition := mustValue(newDefinition(definitionConfig{Name: "test.validator.cancel", Description: "Honor cancellation before accepting output.", CompletionValidator: func(context.Context, CompletionCandidate) (CompletionDecision, error) {
		cancel()
		return CompletionDecision{Accepted: true}, nil
	}}))
	execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
	advance(t, execution, 1)
	turn := testTurn(2)
	turn.Messages = []run.Message{run.Completion{Effect: testTurn(1).EffectID(0), Outcome: mustValue((modelCallResult{Response: finalResponse("candidate")}).settlement(run.MaxPayloadBytes))}}
	if transition, err := execution.Step(ctx, turn); !errors.Is(err, context.Canceled) || transition.Valid() {
		t.Fatal("canceled validation completed the process", err)
	}
}

func TestModelResponseBudgetAdmitsOnlyCanonicalBoundedSettlements(t *testing.T) {
	response := finalResponse(strings.Repeat("<>&\u2028\u2029", 20))
	encoded := mustValue((modelCallResult{Response: response}).settlement(run.MaxPayloadBytes))
	for _, offset := range []int{-1, 0, 1} {
		limit := len(encoded.Payload()) + offset
		settlement, err := (modelCallResult{Response: response}).settlement(limit)
		if offset < 0 {
			if !errors.Is(err, ErrModelResponseTooLarge) || settlement.Valid() {
				t.Fatal("oversized canonical outcome admitted", err)
			}
		} else if err != nil || string(settlement.Payload()) != string(encoded.Payload()) {
			t.Fatal("budget boundary changed outcome bytes", err)
		}
	}
	for _, result := range []modelCallResult{{}, {Response: &chat.Response{}}} {
		if _, err := result.settlement(run.MaxPayloadBytes); !errors.Is(err, ErrInvalidProtocol) {
			t.Fatal("invalid model outcome was encoded", err)
		}
	}
}
