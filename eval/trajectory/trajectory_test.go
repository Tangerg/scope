package trajectory_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

const testBuild = "trajectory-test"

type fixtureInput struct {
	Value      string `json:"value"`
	NullOutput bool   `json:"null_output,omitzero"`
}

type fixtureOutput struct {
	Value string `json:"value"`
}

type fixtureDefinition struct{ descriptor run.Descriptor }

func (f fixtureDefinition) Descriptor() run.Descriptor { return f.descriptor }

func (fixtureDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (fixtureDefinition) Start(input run.Payload) (run.Execution, error) {
	value, err := input.Decode[fixtureInput]()
	if err != nil {
		return nil, err
	}
	return &fixtureExecution{Value: value.Value, NullOutput: value.NullOutput}, nil
}

func (fixtureDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	var execution fixtureExecution
	if err := jsonv2.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

type fixtureExecution struct {
	NullOutput bool   `json:"null_output,omitzero"`
	Value      string `json:"value"`
	Done       bool   `json:"done"`
}

func (f *fixtureExecution) Step(context.Context, run.Turn) (run.Transition, error) {
	if f.Done {
		return run.Transition{}, run.ErrInvalidExecutionState
	}
	f.Done = true
	var value *fixtureOutput
	if !f.NullOutput {
		value = &fixtureOutput{Value: f.Value}
	}
	output, err := run.EncodePayload(value)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(0, output)
}

func (f *fixtureExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("test.trajectory", f)
}

type rejectingDispatcher struct{}

func (rejectingDispatcher) Dispatch(
	context.Context,
	run.EffectRequest,
	run.DeltaEmitter,
) (run.Settlement, error) {
	return run.Settlement{}, errors.New("test dispatcher received an unexpected effect")
}

func (rejectingDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

type fixtureInteractionClient struct {
	calls        atomic.Uint32
	callID       string
	secondCallID string
	city         string
	usage        *chat.Usage
}

func (f *fixtureInteractionClient) Call(context.Context, *chat.Request) (*chat.Response, error) {
	if f.calls.Add(1) == 1 {
		callID, city := f.callID, f.city
		if callID == "" {
			callID = "weather-call"
		}
		if city == "" {
			city = "Paris"
		}
		parts := []chat.Part{chat.NewToolCallPart(chat.ToolCall{
			ID: callID, Name: "weather", Arguments: fmt.Sprintf(`{"city":%q}`, city),
		})}
		if f.secondCallID != "" {
			parts = append(parts, chat.NewToolCallPart(chat.ToolCall{ID: f.secondCallID, Name: "weather", Arguments: `{"city":"Berlin"}`}))
		}
		message := chat.NewAssistantMessage(parts...)
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonToolCalls,
		}, Metadata: &chat.ResponseMetadata{Usage: f.usage}}, nil
	}
	message := chat.NewAssistantMessage(chat.NewTextPart("sunny"))
	return &chat.Response{Output: &chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}, Metadata: &chat.ResponseMetadata{Usage: f.usage}}, nil
}

func (*fixtureInteractionClient) Stream(
	context.Context,
	*chat.Request,
) iter.Seq2[*chat.ResponseDelta, error] {
	return func(func(*chat.ResponseDelta, error) bool) {}
}

type fixtureWeatherTool struct {
	failure error
	release <-chan struct{}
}

func (fixtureWeatherTool) Definition() tool.Definition {
	return tool.Definition{
		Name: "weather", Description: "Return deterministic fixture weather.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"city":{"type":"string"}},
			"required":["city"],
			"additionalProperties":false
		}`),
	}
}

func (f fixtureWeatherTool) Call(ctx context.Context, _ tool.Invocation) (tool.Result, error) {
	if f.release != nil {
		select {
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		case <-f.release:
		}
	}
	if f.failure != nil {
		return tool.Result{}, f.failure
	}
	return tool.Result{Output: tool.NewTextOutput("sunny")}, nil
}

func newRuntime(t *testing.T, store run.Store) *run.Runtime {
	t.Helper()
	runtime, err := run.New(t.Context(), run.Config{Store: store, Build: testBuild})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return runtime
}
func runTrajectory(t *testing.T) trajectory.Trajectory {
	t.Helper()
	return runTrajectoryInput(t, fixtureInput{Value: "done"})
}
func runTrajectoryInput(t *testing.T, value fixtureInput) trajectory.Trajectory {
	t.Helper()
	runtime := newRuntime(t, run.NewMemoryStore())
	inputSchema, err := jsonschema.For[fixtureInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := jsonschema.For[fixtureOutput]()
	if err != nil {
		t.Fatal(err)
	}
	if value.NullOutput {
		outputSchema, err = jsonschema.Parse([]byte(`{"type":"null"}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "test.trajectory", Description: "Complete one deterministic trajectory.", InputSchema: inputSchema, OutputSchema: outputSchema})
	if err != nil {
		t.Fatal(err)
	}
	program, err := run.Bind[run.Payload, run.Payload](runtime.Compiler(), run.BindingConfig{Definition: fixtureDefinition{descriptor: descriptor}, Dispatcher: rejectingDispatcher{}, Revision: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := run.EncodePayload(value)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "trajectory", Key: strings.ReplaceAll(t.Name(), "/", ":")}, Input: payload})
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	recorded, err := trajectory.Capture(t.Context(), handle, &trajectory.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}
func startInteraction(t *testing.T, model chat.Model, tools []tool.Tool, store run.Store) (*run.Handle[interaction.Output], *run.Runtime, run.Program[interaction.Input, interaction.Output]) {
	t.Helper()
	runtime := newRuntime(t, store)
	config := interaction.Config{Name: "test.interaction", Description: "Exercise logged model and tool calls.", Revision: "fixture", Model: model, MaxModelCalls: run.NewQuota(2)}
	if len(tools) > 0 {
		var err error
		config.Tools, err = interaction.NewToolSet(interaction.ToolSetConfig{Revision: "fixture-tools", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
	}
	program, err := interaction.Compile(runtime.Compiler(), config)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[interaction.Input]{ID: run.StartID{Scope: "trajectory", Key: strings.ReplaceAll(t.Name(), "/", ":")}, Input: interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("retained question"))}}})
	if err != nil {
		t.Fatal(err)
	}
	return handle, runtime, program
}
func interactionCoverage(t *testing.T, log run.Log) *trajectory.Coverage {
	t.Helper()
	coverage := new(trajectory.Coverage)
	for _, entry := range log.Entries {
		intent, ok := entry.Record.(run.EffectIntent)
		if !ok || intent.Effect.Target() != run.EffectTargetDispatcher {
			continue
		}
		_, model, err := interaction.ModelRequestFromEffect(intent.Effect)
		if err != nil {
			t.Fatal(err)
		}
		if model {
			coverage.Models = append(coverage.Models, intent.ID)
			continue
		}
		_, _, call, err := interaction.ToolCallFromEffect(intent.Effect)
		if err != nil {
			t.Fatal(err)
		}
		if call {
			coverage.Tools = append(coverage.Tools, intent.ID)
		} else {
			coverage.Other = append(coverage.Other, intent.ID)
		}
	}
	return coverage
}
func captureInteraction(t *testing.T, handle *run.Handle[interaction.Output]) trajectory.Trajectory {
	t.Helper()
	raw, err := trajectory.Capture(t.Context(), handle, nil)
	if err != nil {
		t.Fatal(err)
	}
	log, err := raw.Log()
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := trajectory.New(trajectory.Config{Log: log, Coverage: interactionCoverage(t, log)})
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}
func coveredInteraction(t *testing.T) trajectory.Trajectory {
	t.Helper()
	client := &fixtureInteractionClient{usage: &chat.Usage{InputTokens: 3, OutputTokens: 2}}
	handle, _, _ := startInteraction(t, client, []tool.Tool{fixtureWeatherTool{}}, run.NewMemoryStore())
	if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	return captureInteraction(t, handle)
}
func trajectoryConfig(t *testing.T, recorded trajectory.Trajectory) trajectory.Config {
	t.Helper()
	log, err := recorded.Log()
	if err != nil {
		t.Fatal(err)
	}
	return trajectory.Config{Log: log, Coverage: recorded.Coverage()}
}
func recordedOutput(t *testing.T, recorded trajectory.Trajectory) run.Payload {
	t.Helper()
	output, ok := recorded.Termination().Output()
	if !ok {
		t.Fatal("output is absent")
	}
	return output
}
func rawOutputProjection(output run.Payload) (json.RawMessage, error) { return output.JSON(), nil }
func semanticInteractionOutput(output run.Payload) (json.RawMessage, error) {
	value, err := output.Decode[interaction.Output]()
	if err != nil {
		return nil, err
	}
	return jsonv2.Marshal(value.ModelResponse.Text())
}

func TestCommittedLogProjectsModelToolUsageAndEvaluator(t *testing.T) {
	recorded := coveredInteraction(t)
	if err := recorded.CheckCoverage(); err != nil {
		t.Fatal(err)
	}
	models, tools := recorded.ModelCalls(), recorded.ToolCalls()
	if len(models) != 2 || len(tools) != 1 || models[0].EffectID.Process != tools[0].EffectID.Process {
		t.Fatalf("calls not projected in one interaction: %+v %+v", models, tools)
	}
	if models[0].Request == nil || len(models[0].Request.Tools) != 1 || models[0].Request.Tools[0].Name != "weather" {
		t.Fatal("exact dispatched tool advertisement missing")
	}
	if models[0].Settlement.Outcome != interaction.ModelOutcomeSucceeded || tools[0].Outcome() != interaction.ToolOutcomeSucceeded {
		t.Fatal("settlements changed")
	}
	usage, err := recorded.TreeUsage()
	if err != nil || usage.PreparedEffects != 3 || usage.CommittedSteps == 0 || usage.AcceptedMessages != 3 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	tokens, err := recorded.TotalTokens()
	if err != nil || tokens != 10 {
		t.Fatalf("tokens=%d error=%v", tokens, err)
	}
	maximumSteps, maximumTokens := usage.CommittedSteps, int64(10)
	report, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: recorded, Expected: trajectory.Expectation{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "weather", Outcome: interaction.ToolOutcomeSucceeded}}}, Limits: trajectory.Limits{CommittedSteps: &maximumSteps, TotalTokens: &maximumTokens}}})
	if err != nil || report.Verdict() != eval.VerdictPass {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	maximumTokens = 9
	report, err = (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: recorded, Expected: trajectory.Expectation{Status: run.StatusCompleted, Limits: trajectory.Limits{TotalTokens: &maximumTokens}}})
	if err != nil || report.Verdict() != eval.VerdictFail {
		t.Fatalf("cost regression=%+v error=%v", report, err)
	}
}

func TestPortableLogOwnsEveryOutcomeAndDetachedProjection(t *testing.T) {
	recorded := coveredInteraction(t)
	before, err := jsonv2.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	config := trajectoryConfig(t, recorded)
	first := config.Log.Entries[0].Record.(run.Started)
	first.Input, _ = run.EncodePayload("changed input")
	config.Log.Entries[0].Record = first
	config.Coverage.Models = nil
	models, tools := recorded.ModelCalls(), recorded.ToolCalls()
	models[0].Request.Messages = nil
	models[0].Settlement.Response.Output.Message.Parts = nil
	tools[0].Settlement.Result.Output = tool.NewTextOutput("changed result")
	after, err := jsonv2.Marshal(recorded)
	if err != nil || string(after) != string(before) {
		t.Fatal("read projection changed authoritative evidence")
	}
	var restored trajectory.Trajectory
	if decodeErr := jsonv2.Unmarshal(before, &restored); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	digest, err := recorded.BehaviorDigest(semanticInteractionOutput)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := restored.BehaviorDigest(semanticInteractionOutput)
	if err != nil || roundtrip != digest {
		t.Fatalf("behavior changed after round trip: %v", err)
	}
	if tokens, err := restored.TotalTokens(); err != nil || tokens != 10 {
		t.Fatal(tokens, err)
	}
}

func TestBehaviorIgnoresProviderIDsAndTokenAccountingButRetainsArguments(t *testing.T) {
	collect := func(id, city string, tokens int64) trajectory.Trajectory {
		client := &fixtureInteractionClient{callID: id, city: city, usage: &chat.Usage{InputTokens: tokens}}
		handle, _, _ := startInteraction(t, client, []tool.Tool{fixtureWeatherTool{}}, run.NewMemoryStore())
		if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
			t.Fatal(awaitErr)
		}
		return captureInteraction(t, handle)
	}
	baseline := collect("provider-a", "Paris", 5)
	same := collect("provider-b", "Paris", 100)
	changed := collect("provider-c", "London", 5)
	first, err := baseline.BehaviorDigest(semanticInteractionOutput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := same.BehaviorDigest(semanticInteractionOutput)
	if err != nil || first != second {
		t.Fatalf("provider metadata changed behavior: %s %s %v", first, second, err)
	}
	third, err := changed.BehaviorDigest(semanticInteractionOutput)
	if err != nil || first == third {
		t.Fatalf("tool arguments were lost: %v", err)
	}
}

func TestEvaluatorCancellationAndInvalidArgumentsRemainErrors(t *testing.T) {
	actual := runTrajectory(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (trajectory.Evaluator{}).Evaluate(ctx, trajectory.Sample{Actual: actual, Expected: trajectory.Expectation{Status: run.StatusCompleted}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, invalid := range []trajectory.ToolArguments{`{"a":1,"a":2}`, `{"a":`, "\xff"} {
		if err := invalid.Validate(); !errors.Is(err, trajectory.ErrInvalidSample) {
			t.Fatalf("admitted %q: %v", invalid, err)
		}
	}
}

func TestLimitsJSONPreservesNanoseconds(t *testing.T) {
	for _, elapsed := range []time.Duration{0, 9007199254740993} {
		limits := trajectory.Limits{Elapsed: &elapsed}
		encoded, err := jsonv2.Marshal(limits)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf(`{"elapsed_ns":%d}`, elapsed)
		if string(encoded) != want {
			t.Fatalf("limits = %s, want %s", encoded, want)
		}
		var restored trajectory.Limits
		if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Elapsed == nil || *restored.Elapsed != elapsed {
			t.Fatalf("restored = %+v", restored)
		}
	}
}
