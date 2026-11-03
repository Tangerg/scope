package trajectory_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestReferenceBaselineAndResourceLimitsRemainIndependentAssertions(t *testing.T) {
	baseline := runTrajectory(t)
	changed := runTrajectoryInput(t, fixtureInput{Value: "changed"})
	for _, test := range []struct {
		name   string
		actual trajectory.Trajectory
		want   eval.Verdict
	}{{"same", baseline, eval.VerdictPass}, {"changed", changed, eval.VerdictFail}} {
		t.Run(test.name, func(t *testing.T) {
			usage, err := test.actual.TreeUsage()
			if err != nil {
				t.Fatal(err)
			}
			report, err := (trajectory.Evaluator{OutputProjection: rawOutputProjection}).Evaluate(t.Context(), trajectory.Sample{Actual: test.actual, Expected: trajectory.Expectation{Status: run.StatusCompleted, Baseline: &baseline, Limits: trajectory.Limits{CommittedSteps: &usage.CommittedSteps, PreparedEffects: &usage.PreparedEffects, AcceptedMessages: &usage.AcceptedMessages, Elapsed: new(time.Hour)}}})
			if err != nil || report.Verdict() != test.want {
				t.Fatalf("comparison=%v error=%v", report, err)
			}
		})
	}
	config := trajectoryConfig(t, baseline)
	config.Coverage = nil
	unknown, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (trajectory.Evaluator{OutputProjection: rawOutputProjection}).Evaluate(t.Context(), trajectory.Sample{Actual: baseline, Expected: trajectory.Expectation{Status: run.StatusCompleted, Baseline: &unknown}}); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatal("baseline without coverage produced a comparison", err)
	}
	if _, err := baseline.BehaviorDigest(nil); !errors.Is(err, trajectory.ErrInvalidSample) {
		t.Fatal("missing output projection admitted", err)
	}
	if baseline.RootProcessID() != config.Log.Root {
		t.Fatal("root identity has another owner")
	}
}

func TestStatusToolMismatchAndMissingEvidenceDoNotProduceFalsePasses(t *testing.T) {
	actual := coveredInteraction(t)
	for _, expected := range []trajectory.Expectation{
		{Status: run.StatusFailed},
		{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{}},
		{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "different"}}}},
		{Status: run.StatusCompleted, Limits: trajectory.Limits{Elapsed: new(time.Duration(0))}},
	} {
		report, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: actual, Expected: expected})
		if err != nil || report.Verdict() != eval.VerdictFail {
			t.Fatal("regression became a pass", report, err)
		}
	}
	invalid := trajectory.Sample{Expected: trajectory.Expectation{Status: run.StatusCompleted}}
	if _, err := (trajectory.Evaluator{}).Evaluate(t.Context(), invalid); !errors.Is(err, trajectory.ErrInvalidSample) {
		t.Fatal("absent log admitted", err)
	}
	invalid = trajectory.Sample{Actual: actual}
	if _, err := (trajectory.Evaluator{}).Evaluate(t.Context(), invalid); !errors.Is(err, trajectory.ErrInvalidSample) {
		t.Fatal("absent expectation admitted", err)
	}
	if _, err := jsonv2.Marshal(trajectory.Trajectory{}); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("zero trajectory serialized", err)
	}
	var missing *trajectory.Trajectory
	if err := missing.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("nil receiver admitted", err)
	}
	if _, err := (trajectory.Trajectory{}).TreeUsage(); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("missing log proved usage", err)
	}
	if _, err := (trajectory.Trajectory{}).Log(); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("missing log exported evidence", err)
	}
	if _, err := (trajectory.Trajectory{}).BehaviorDigest(rawOutputProjection); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("missing log produced behavior", err)
	}
}

func TestMalformedModelArgumentsRemainDurableRejectionEvidence(t *testing.T) {
	var calls atomic.Uint32
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		if calls.Add(1) == 1 {
			message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "bad-arguments", Name: "weather", Arguments: `{"city":`}))
			return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, nil
		}
		return completedResponse("repair arguments", &chat.Usage{}), nil
	})
	handle, _, _ := startInteraction(t, model, []tool.Tool{fixtureWeatherTool{}}, run.NewMemoryStore())
	if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	actual := captureInteraction(t, handle)
	if observed := actual.ToolCalls(); len(observed) != 0 {
		t.Fatalf("rejected model arguments invented a dispatched tool: %+v", observed)
	}
	models := actual.ModelCalls()
	if len(models) != 2 || models[0].Settlement.Response.Output.Message.Parts[0].ToolCall.Arguments != `{"city":` {
		t.Fatalf("rejected decision evidence lost: %+v", models)
	}
	report, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: actual, Expected: trajectory.Expectation{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{}}})
	if err != nil || report.Verdict() != eval.VerdictPass {
		t.Fatal(report, err)
	}
	if _, err := actual.BehaviorDigest(semanticInteractionOutput); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("malformed arguments acquired fabricated JSON meaning", err)
	}
}

func TestControlAndFailureEvidenceSurvivesBehaviorProjection(t *testing.T) {
	runtime := newRuntime(t, run.NewMemoryStore())
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	program, err := coordination.Compile[string, run.Signal](runtime.Compiler(), coordination.Config{Name: "test.control", Description: "Wait for explicit input", Policy: coordination.InputGate{RequestSchema: schema, AnswerSchema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "trajectory", Key: "control"}, Input: "question"})
	if err != nil {
		t.Fatal(err)
	}
	var wait run.WaitKey
	for entry, err := range handle.Observe(t.Context(), 0) {
		if err != nil {
			t.Fatal(err)
		}
		if step, ok := entry.Record.(run.Stepped); ok && len(step.Transition.Waits()) != 0 {
			wait = step.Transition.Waits()[0].Key
			break
		}
	}
	if pauseErr := handle.Pause(t.Context(), "hold answer"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	if resumeErr := handle.Resume(t.Context()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	if answerErr := handle.Answer(t.Context(), wait, "answer"); answerErr != nil {
		t.Fatal(answerErr)
	}
	if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	actual, err := trajectory.Capture(t.Context(), handle, &trajectory.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	if _, digestErr := actual.BehaviorDigest(func(run.Payload) (json.RawMessage, error) { return json.RawMessage(`"answered"`), nil }); digestErr != nil {
		t.Fatal(digestErr)
	}
	stage, err := workflow.Transform("fail", func(context.Context, string) (string, error) { return "", errors.New("known execution failure") })
	if err != nil {
		t.Fatal(err)
	}
	failing, err := workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.failure", Description: "Fail deterministically", Revision: "failure", Stages: []workflow.Stage{stage}})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runtime.Start(t.Context(), failing, run.StartRequest[string]{ID: run.StartID{Scope: "trajectory", Key: "failure"}, Input: "input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := failed.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	recorded, err := trajectory.Capture(t.Context(), failed, &trajectory.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorded.BehaviorDigest(rawOutputProjection); err != nil {
		t.Fatal(err)
	}
}

func TestNestedDelegateBehaviorIgnoresProviderIdentityAndPublicationTime(t *testing.T) {
	collect := func(callID string) trajectory.Trajectory {
		runtime := newRuntime(t, run.NewMemoryStore())
		leaf, err := interaction.Compile(runtime.Compiler(), interaction.Config{Name: "test.leaf", Description: "Answer one delegated question", Revision: "leaf", Model: interaction.CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			response := completedResponse("leaf answer", &chat.Usage{InputTokens: 2})
			response.Metadata.ID = callID
			return response, nil
		}))})
		if err != nil {
			t.Fatal(err)
		}
		prepare, err := workflow.Transform("prepare", func(context.Context, struct{}) (interaction.Input, error) {
			return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("leaf question"))}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		call, err := workflow.Call(workflow.CallConfig{ID: "leaf", Child: run.ChildBinding{Program: leaf.Erase()}})
		if err != nil {
			t.Fatal(err)
		}
		finish, err := workflow.Transform("text", func(_ context.Context, value interaction.Output) (string, error) {
			return value.ModelResponse.Text(), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		composite, err := workflow.Compile[struct{}, string](runtime.Compiler(), workflow.Config{Name: "test.delegate", Description: "Adapt a delegated conversation", Revision: "adapt", Stages: []workflow.Stage{prepare, call, finish}})
		if err != nil {
			t.Fatal(err)
		}
		delegate, err := interaction.NewDelegate(interaction.DelegateConfig{Name: "delegate", Description: "Answer through a child", Child: run.ChildBinding{Program: composite.Erase()}})
		if err != nil {
			t.Fatal(err)
		}
		var requests atomic.Uint32
		model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			if requests.Add(1) == 1 {
				message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: callID, Name: "delegate", Arguments: `{}`}))
				return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, nil
			}
			return completedResponse("parent answer", &chat.Usage{}), nil
		})
		program, err := interaction.Compile(runtime.Compiler(), interaction.Config{Name: "test.parent", Description: "Delegate then answer", Revision: "parent", Model: interaction.CallModel(model), Delegates: []interaction.Delegate{delegate}})
		if err != nil {
			t.Fatal(err)
		}
		handle, err := runtime.Start(t.Context(), program, run.StartRequest[interaction.Input]{ID: run.StartID{Scope: "trajectory", Key: callID}, Input: interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("delegate"))}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
			t.Fatal(awaitErr)
		}
		actual := captureInteraction(t, handle)
		if len(actual.ModelCalls()) != 3 || len(actual.ToolCalls()) != 0 {
			t.Fatal("delegate gained a separate ordinary-tool lifecycle")
		}
		return actual
	}
	first, second := collect("provider-a"), collect("provider-b")
	before, err := first.BehaviorDigest(semanticInteractionOutput)
	if err != nil {
		t.Fatal(err)
	}
	after, err := second.BehaviorDigest(semanticInteractionOutput)
	if err != nil || before != after {
		t.Fatalf("provider identity changed nested behavior: %s %s %v", before, after, err)
	}
	config := trajectoryConfig(t, first)
	for _, entry := range config.Log.Entries {
		if intent, ok := entry.Record.(run.EffectIntent); ok && intent.Effect.Target() == run.EffectTargetFramework {
			config.Coverage.Other = append(config.Coverage.Other, intent.ID)
			break
		}
	}
	if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("kernel effect was accepted as dispatcher evidence", err)
	}
}
