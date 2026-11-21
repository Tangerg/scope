package interaction

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// contextPreparerFunc prepares the context before every model call.
type contextPreparerFunc func(context.Context, ContextInvocation, *chat.Request) ([]chat.Message, error)

func (contextPreparerFunc) PreparationTrigger() func(uint64, *chat.Request) bool {
	return func(uint64, *chat.Request) bool { return true }
}

func (c contextPreparerFunc) PrepareContext(ctx context.Context, invocation ContextInvocation, request *chat.Request) ([]chat.Message, error) {
	return c(ctx, invocation, request)
}

// unusedPreparer configures a definition whose preparation never runs.
var unusedPreparer = contextPreparerFunc(func(context.Context, ContextInvocation, *chat.Request) ([]chat.Message, error) {
	return nil, errors.New("must not prepare")
})

func TestPreparationAndDeferredToolsRestoreAtEveryCommittedBoundary(t *testing.T) {
	advertiser := scriptTool{name: "discover", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput("discovered")}, nil
	}, advertise: func(tool.Invocation, tool.Result) ([]string, error) {
		return []string{"hidden"}, nil
	}}
	hidden := scriptTool{name: "hidden", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput("final evidence")}, nil
	}}
	tools, err := NewToolSet(ToolSetConfig{Tools: []tool.Tool{advertiser}, DeferredTools: []tool.Tool{hidden}})
	if err != nil {
		t.Fatal(err)
	}
	policy := func(request *chat.Request) ([]chat.Message, error) {
		return append(request.Messages, chat.NewUserMessage(chat.NewTextPart("local policy"))), nil
	}
	definition, err := newDefinition(Config{Name: "test.context.discovery", Description: "Prepare context before using discovered tools.", Tools: tools, ContextPolicy: policy, ContextPreparer: unusedPreparer})
	if err != nil {
		t.Fatal(err)
	}
	var prepared []ContextInvocation
	preparer := contextPreparerFunc(func(_ context.Context, invocation ContextInvocation, request *chat.Request) ([]chat.Message, error) {
		if request.Messages[len(request.Messages)-1].Text() != "local policy" || invocation.Sequence != uint64(len(prepared)+1) {
			return nil, errors.New("preparation was not bound to the next model call")
		}
		prepared = append(prepared, invocation)
		request.Tools[0].Name = "changed only in preparer copy"
		return append(request.Messages, chat.NewUserMessage(chat.NewTextPart("retrieved evidence"))), nil
	})
	models := 0
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		models++
		if request.Messages[len(request.Messages)-1].Text() != "retrieved evidence" {
			return nil, errors.New("model bypassed committed prepared context")
		}
		names := make([]string, len(request.Tools))
		for index, definition := range request.Tools {
			names[index] = definition.Name
		}
		if models == 1 {
			if !slices.Equal(names, []string{"discover"}) {
				return nil, errors.New("hidden tool advertised before discovery")
			}
			return callResponse(requested("discover", "discovery")), nil
		}
		if !slices.Equal(names, []string{"discover", "hidden"}) {
			return nil, errors.New("prepared request changed tool ownership")
		}
		return callResponse(requested("hidden", "use-evidence")), nil
	})
	dispatcher, err := newDispatcher(definition, Config{Model: CallModel(model), Tools: tools, ContextPreparer: preparer})
	if err != nil {
		t.Fatal(err)
	}
	result := conformancetest.Run(t, run.BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "context-discovery"}, run.Config{}, mustValue(run.EncodePayload(testInput())))
	output, err := result.Output()
	if err != nil {
		t.Fatal(err)
	}
	value, err := output.Decode[Output]()
	if err != nil || len(value.DirectToolResults) != 1 || value.DirectToolResults[0].ID != "use-evidence" || models != 2 || len(prepared) != 2 {
		t.Fatalf("output=%+v models=%d preparations=%d error=%v", value, models, len(prepared), err)
	}
	if prepared[0].Effect.ID() == prepared[1].Effect.ID() || prepared[0].Effect.Effect().Target() != run.EffectTargetDispatcher {
		t.Fatal("context preparation did not retain separate effect identities")
	}
}

func TestUnknownContextCanBeResolvedWithoutRepeatingPreparation(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	preparations, models := 0, 0
	preparer := contextPreparerFunc(func(context.Context, ContextInvocation, *chat.Request) ([]chat.Message, error) {
		preparations++
		return nil, errors.New("retrieval acknowledgement lost")
	})
	program := compileTest(t, runtime, Config{Revision: "preparer", ContextPreparer: preparer, Model: CallModel(chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		models++
		return finalResponse(request.Messages[len(request.Messages)-1].Text()), nil
	}))})
	handle := startInteraction(t, runtime, program)
	var pending run.PendingEffect
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		view, err := log.Inspect()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range view.Processes {
			for _, effect := range process.PendingEffects {
				if effect.Outcome.Valid() && effect.Outcome.Status() == run.SettlementStatusUnknown {
					pending = effect
					return true
				}
			}
		}
		return false
	})
	if models != 0 || pending.Failure == nil {
		t.Fatal("uncertain context reached the model or lost failure evidence")
	}
	if _, err := SettlePreparedContext(program, pending.Request, nil); err == nil {
		t.Fatal("invalid replacement context admitted")
	}
	settlement, err := SettlePreparedContext(program, pending.Request, []chat.Message{chat.NewUserMessage(chat.NewTextPart("investigated evidence"))})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.ResolveUnknownEffect(t.Context(), pending.Request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	output := awaitOutput(t, handle)
	if output.ModelResponse.Text() != "investigated evidence" || preparations != 1 || models != 1 {
		t.Fatalf("output=%+v preparation=%d models=%d", output, preparations, models)
	}
}

func TestInvalidContextFailsBeforeModelDispatch(t *testing.T) {
	cause := errors.New("retrieval denied")
	// The Step terminates with the Failure its settlement recorded: a Host
	// refusal stays external, while a contract breach stays a contract defect.
	for _, row := range []struct {
		name     string
		preparer contextPreparerFunc
		limit    int
		want     run.FailureClassification
	}{
		{"definite refusal", func(context.Context, ContextInvocation, *chat.Request) ([]chat.Message, error) {
			failure := &ContextFailure{Cause: cause}
			if !errors.Is(failure, cause) || !strings.Contains(failure.Error(), cause.Error()) {
				t.Fatal("context failure lost cause")
			}
			return nil, failure
		}, 0, FailureContextRejected},
		{"invalid messages", func(context.Context, ContextInvocation, *chat.Request) ([]chat.Message, error) { return nil, nil }, 0, FailureDispatchRejected},
		{"oversized messages", func(context.Context, ContextInvocation, *chat.Request) ([]chat.Message, error) {
			return []chat.Message{chat.NewUserMessage(chat.NewTextPart(strings.Repeat("evidence", 100)))}, nil
		}, 128, FailureDispatchRejected},
	} {
		t.Run(row.name, func(t *testing.T) {
			runtime := conformancetest.Runtime(t, run.Config{})
			program := compileTest(t, runtime, Config{Revision: "preparer", MaxResponseBytes: row.limit, ContextPreparer: row.preparer, Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				t.Error("invalid prepared context dispatched to model")
				return finalResponse("unexpected"), nil
			}))})
			result, err := startInteraction(t, runtime, program).Await(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			failure, ok := result.Termination().Failure()
			if !ok || failure.Classification() != row.want {
				t.Fatalf("termination=%+v", result.Termination())
			}
		})
	}
}

func TestCompletionValidatorRetriesWithOwnedCandidateCopies(t *testing.T) {
	tools := toolSetTest(t, scriptTool{name: "direct", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput("evidence")}, nil
	}})
	definition, err := newDefinition(Config{Name: "test.validated", Description: "Validate the semantic result.", Tools: tools, MaxModelCalls: run.NewQuota(3), CompletionValidator: func(candidate CompletionCandidate) (CompletionDecision, error) {
		contextCopy := candidate.WorkingContext()
		contextCopy.Messages[0] = chat.NewUserMessage(chat.NewTextPart("mutated"))
		if candidate.WorkingContext().Messages[0].Text() == "mutated" || len(candidate.Artifacts()) != 0 {
			return CompletionDecision{}, errors.New("candidate exposes mutable or fabricated evidence")
		}
		output := candidate.Output()
		if len(output.DirectToolResults) != 0 {
			output.DirectToolResults[0].ID = "mutated"
			if candidate.Output().DirectToolResults[0].ID == "mutated" {
				return CompletionDecision{}, errors.New("direct result aliases candidate")
			}
			return RejectCompletion("Explain the evidence.")
		}
		output.ModelResponse.Output.Message = nil
		if candidate.Output().ModelResponse.Text() != "explained" {
			return CompletionDecision{}, errors.New("response aliases candidate")
		}
		return AcceptCompletion(), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	models := 0
	dispatcher, err := newDispatcher(definition, Config{Tools: tools, Model: CallModel(chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			return callResponse(requested("direct", "evidence")), nil
		}
		if request.Messages[len(request.Messages)-1].Text() != "Explain the evidence." {
			return nil, errors.New("retry feedback absent")
		}
		return finalResponse("explained"), nil
	}))})
	if err != nil {
		t.Fatal(err)
	}
	result := conformancetest.Run(t, run.BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "validation"}, run.Config{}, mustValue(run.EncodePayload(testInput())))
	output, err := result.Output()
	if err != nil {
		t.Fatal(err)
	}
	value, err := output.Decode[Output]()
	if err != nil || value.ModelResponse.Text() != "explained" || value.ModelCalls != 2 {
		t.Fatalf("output=%+v error=%v", value, err)
	}
}

func TestCompletionFailureClassificationAndModelCallBound(t *testing.T) {
	for _, row := range []struct {
		name      string
		validator CompletionValidator
		want      run.FailureClassification
	}{
		{"invalid decision", func(CompletionCandidate) (CompletionDecision, error) {
			return CompletionDecision{}, nil
		}, FailureCompletionDecisionInvalid},
		{"callback failure", func(CompletionCandidate) (CompletionDecision, error) {
			return CompletionDecision{}, errors.New("evidence cannot be validated")
		}, FailureCompletionValidatorFailed},
		{"bounded retry", func(CompletionCandidate) (CompletionDecision, error) {
			return RejectCompletion("Add evidence.")
		}, FailureModelCallLimit},
	} {
		t.Run(row.name, func(t *testing.T) {
			runtime := conformancetest.Runtime(t, run.Config{})
			models := 0
			program := compileTest(t, runtime, Config{Revision: "validator", CompletionValidator: row.validator, MaxModelCalls: run.NewQuota(1), Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				models++
				return finalResponse("candidate"), nil
			}))})
			result, err := startInteraction(t, runtime, program).Await(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			failure, ok := result.Termination().Failure()
			if !ok || failure.Classification() != row.want || models != 1 {
				t.Fatalf("failure=%+v models=%d", failure, models)
			}
		})
	}
}

func TestCompletionDecisionRejectsAmbiguousOrUnboundedFeedback(t *testing.T) {
	if !AcceptCompletion().Valid() || (CompletionDecision{}).Valid() {
		t.Fatal("acceptance or the zero decision was misclassified")
	}
	for _, feedback := range []string{"Add evidence.", "补充证据", strings.Repeat("界", 1365) + "a"} {
		decision, err := RejectCompletion(feedback)
		if err != nil || !decision.Valid() || decision.Accepted() || decision.Feedback() != feedback {
			t.Fatalf("RejectCompletion(%q) = %+v, %v", feedback, decision, err)
		}
	}
	for _, feedback := range []string{"", " retry", "\xff", strings.Repeat("界", 1366)} {
		if _, err := RejectCompletion(feedback); !errors.Is(err, ErrInvalidCompletionDecision) {
			t.Fatalf("RejectCompletion(%q) error = %v", feedback, err)
		}
	}
}
