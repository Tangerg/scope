package trajectory_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestRecorderAndEvaluatorCoverAgentRegressionDimensions(t *testing.T) {
	recorder := &trajectory.Recorder{}
	process := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{})
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	coverage := &trajectory.Coverage{}
	for _, event := range recorded.Events() {
		if event.Name() != agent.EventProcessStarted {
			continue
		}
		if event.Relation().IsRoot() {
			coverage.Models = append(coverage.Models, event.DeploymentRef())
		} else {
			coverage.Tools = append(coverage.Tools, event.DeploymentRef())
		}
	}
	calls := recorded.ModelCalls()
	for i := range calls {
		calls[i].Response.Metadata = &chat.ResponseMetadata{Usage: &chat.Usage{InputTokens: 3, OutputTokens: 2}}
	}
	recorded, err = trajectory.New(trajectory.Config{RootProcessID: recorded.RootProcessID(), Termination: recorded.Termination(), Output: recorded.Output(), RootUsage: recorded.RootUsage(), Elapsed: recorded.Elapsed(), Coverage: coverage, Events: recorded.Events(), ModelCalls: calls, ToolCalls: recorded.ToolCalls()})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := recorded.Clone()
	if err != nil {
		t.Fatal(err)
	}
	usage, err := recorded.TreeUsage()
	if err != nil {
		t.Fatal(err)
	}
	steps := usage.CommittedSteps
	tokens := int64(10)
	duration := time.Hour
	paris := trajectory.ToolArguments(`{"city":"Paris"}`)
	report, err := (trajectory.Evaluator{OutputProjection: rawOutputProjection}).Evaluate(t.Context(), trajectory.Sample{
		Actual: recorded,
		Expected: trajectory.Expectation{
			Status: agent.StatusCompleted,
			Output: recorded.Output(),
			Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{
				Name: "weather", Arguments: &paris,
				Outcome: trajectory.ToolOutcomeSucceeded,
			}}},
			Baseline: &baseline,
			Limits: trajectory.Limits{
				CommittedSteps: &steps, TotalTokens: &tokens, Elapsed: &duration,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != eval.VerdictPass || len(report.Details) != 6 {
		t.Fatalf("trajectory report = verdict %s with %d details", report.Verdict, len(report.Details))
	}

	noSteps := uint64(0)
	berlin := trajectory.ToolArguments(`{"city":"Berlin"}`)
	report, err = (trajectory.Evaluator{OutputProjection: rawOutputProjection}).Evaluate(t.Context(), trajectory.Sample{
		Actual: recorded,
		Expected: trajectory.Expectation{
			Status: agent.StatusCompleted,
			Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{
				Name: "weather", Arguments: &berlin,
			}}},
			Limits: trajectory.Limits{CommittedSteps: &noSteps},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != eval.VerdictFail {
		t.Fatalf("mismatched Tool call and Step limit verdict = %s, want fail", report.Verdict)
	}
}

func TestRecorderCapturesInteractionModelAndToolFacts(t *testing.T) {
	recorder := &trajectory.Recorder{}
	result := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{})
	recorded, err := recorder.Take(t.Context(), result, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded.ModelCalls()) != 2 || len(recorded.ToolCalls()) != 1 {
		t.Fatalf("recorded %d model calls and %d tool calls", len(recorded.ModelCalls()), len(recorded.ToolCalls()))
	}
	call := recorded.ToolCalls()[0]
	if call.Call.Name != "weather" || call.Call.Arguments != `{"city":"Paris"}` ||
		call.Outcome != trajectory.ToolOutcomeSucceeded || call.Result == nil {
		t.Fatalf("recorded tool call = %#v", call)
	}
}

func runRecordedInteraction(t *testing.T, recorder *trajectory.Recorder, observer interaction.ToolObserver, weather tool.Tool) *agent.Process {
	t.Helper()
	process, _ := startRecordedInteraction(t, recorder, observer, weather, 2)
	_, err := process.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func startRecordedInteraction(t *testing.T, recorder *trajectory.Recorder, observer interaction.ToolObserver, weather tool.Tool, maxModelCalls uint32) (*agent.Process, *agent.Engine) {
	t.Helper()
	toolSet, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "test.trajectory.tools", Description: "Record independently settled Tool calls.", Tools: []tool.Tool{weather}, Observer: observer,
		ImplementationDigest: agent.ComputeDigest([]byte("trajectory-tool-implementation")), ConfigurationDigest: agent.ComputeDigest([]byte("trajectory-tool-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "test.trajectory_interaction", Description: "Exercise trajectory observation boundaries.",
		MaxModelCalls: maxModelCalls, Tools: toolSet, ToolBudget: agent.Budget{Steps: 8, Effects: 4, Signals: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Model: &fixtureInteractionClient{}, Observer: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("trajectory-interaction-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("trajectory-interaction-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{recorder}, DeploymentResolver: trajectoryDeploymentResolver{toolSet.Deployment().DeploymentRef(): toolSet.Deployment()}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("weather"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if killErr := process.Kill(ctx, "release recorded test tree"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
			t.Error(killErr)
		}
		if releaseErr := engine.ReleaseTree(ctx, process.ID()); releaseErr != nil {
			t.Error(releaseErr)
		}
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return process, engine
}

func TestBehaviorDigestExcludesTimingAndProviderAccounting(t *testing.T) {
	baseline := decorateBehaviorTrajectory(t, coveredInteraction(t), "response-a", "call-a", 3)
	candidate := decorateBehaviorTrajectory(t, coveredInteraction(t), "response-b", "call-b", 300)
	if baseline.RootProcessID() == candidate.RootProcessID() {
		t.Fatal("independent runs unexpectedly reused one Process identity")
	}
	left, err := baseline.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	right, err := candidate.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("non-semantic execution identity changed digest: %s != %s", left, right)
	}
	changedCalls := append([]trajectory.ToolCall(nil), candidate.ToolCalls()...)
	changedCalls[0].Call.Arguments = `{"city":"Berlin"}`
	changed, err := trajectory.New(trajectory.Config{
		Coverage:      candidate.Coverage(),
		RootProcessID: candidate.RootProcessID(),
		Termination:   candidate.Termination(),
		Output:        candidate.Output(),
		RootUsage:     candidate.RootUsage(),
		Elapsed:       candidate.Elapsed(),
		Events:        candidate.Events(),
		ModelCalls:    candidate.ModelCalls(),
		ToolCalls:     changedCalls,
	})
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := changed.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == right {
		t.Fatal("semantic tool argument change preserved behavior digest")
	}
}

func decorateBehaviorTrajectory(
	t *testing.T,
	base trajectory.Trajectory,
	responseID string,
	toolCallID string,
	inputTokens int64,
) trajectory.Trajectory {
	t.Helper()
	config := trajectoryConfig(base)
	config.Elapsed = new(*base.Elapsed() + time.Duration(inputTokens))
	for i := range config.ModelCalls {
		config.ModelCalls[i].Response.Metadata = &chat.ResponseMetadata{
			ID: responseID, Model: "fixture", Usage: &chat.Usage{InputTokens: inputTokens, OutputTokens: 2},
		}
	}
	config.ToolCalls[0].Call.ID = toolCallID
	config.ToolCalls[0].Result.ID = toolCallID
	decorated, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	return decorated
}

func TestEvaluatorHonorsCancellationAndRejectsInvalidExpectations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (trajectory.Evaluator{OutputProjection: rawOutputProjection}).Evaluate(ctx, trajectory.Sample{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled evaluation error = %v", err)
	}
	invalid := trajectory.Expectation{
		Status: agent.StatusCompleted,
		Tools:  &trajectory.ToolSequence{},
	}
	invalidArguments := trajectory.ToolArguments(`{} {}`)
	invalid.Tools.Calls = []trajectory.ToolExpectation{{
		Name: "weather", Arguments: &invalidArguments,
	}}
	if err := invalid.Validate(); !errors.Is(err, trajectory.ErrInvalidSample) {
		t.Fatalf("invalid Tool expectation error = %v", err)
	}
}

func TestToolArgumentsRejectAmbiguousJSON(t *testing.T) {
	processID, err := agent.ParseProcessID("process:arguments")
	if err != nil {
		t.Fatal(err)
	}
	effectID, err := agent.ParseEffectID("effect:arguments")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"city":"Paris","city":"Berlin"}`,
		`{"city":"Paris","\u0063ity":"Berlin"}`,
		`{"city":"\ud800"}`,
		"{\"city\":\"\xff\"}",
	} {
		t.Run(raw, func(t *testing.T) {
			arguments := trajectory.ToolArguments(raw)
			if validateErr := arguments.Validate(); !errors.Is(validateErr, trajectory.ErrInvalidSample) {
				t.Fatalf("ToolArguments.Validate = %v, want ErrInvalidSample", validateErr)
			}
			call := trajectory.ToolCall{
				EffectID: effectID, ProcessID: processID, StepSequence: 1, ModelCall: 1,
				Call:    chat.ToolCall{ID: "call-1", Name: "weather", Arguments: raw},
				Outcome: trajectory.ToolOutcomeUnknown,
			}
			if validateErr := call.Validate(); !errors.Is(validateErr, trajectory.ErrInvalidTrajectory) {
				t.Fatalf("ToolCall.Validate = %v, want ErrInvalidTrajectory", validateErr)
			}
		})
	}
}

func TestTrajectoryJSONRoundTripPreservesCanonicalBehavior(t *testing.T) {
	recorded := runTrajectory(t)
	encoded, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	var decoded trajectory.Trajectory
	if decodeErr := json.Unmarshal(encoded, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	want, err := recorded.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decoded.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decoded behavior digest = %s, want %s", got, want)
	}
	if *decoded.Elapsed() != *recorded.Elapsed() {
		t.Fatalf("decoded duration = %s, want %s", decoded.Elapsed(), recorded.Elapsed())
	}
	unknown := []byte(string(encoded[:len(encoded)-1]) + `,"unexpected":true}`)
	if decodeErr := json.Unmarshal(unknown, &decoded); !errors.Is(decodeErr, jsonv2.ErrUnknownName) {
		t.Fatalf("decode error = %v, want unknown object member", decodeErr)
	}
	retained, marshalErr := json.Marshal(decoded)
	if marshalErr != nil || string(retained) != string(encoded) {
		t.Fatalf("rejected decode changed trajectory: %s, error = %v", retained, marshalErr)
	}
	if err := json.Unmarshal([]byte(`{}`), &decoded); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("invalid trajectory JSON error = %v", err)
	}
}

type fixtureInput struct {
	Value string `json:"value"`
}

type fixtureOutput struct {
	Value string `json:"value"`
}

type fixtureDefinition struct{ descriptor agent.Descriptor }

func (f fixtureDefinition) Descriptor() agent.Descriptor { return f.descriptor }

func (fixtureDefinition) Start(input agent.Input) (agent.Execution, error) {
	value, err := input.Decode[fixtureInput]()
	if err != nil {
		return nil, err
	}
	return &fixtureExecution{Value: value.Value}, nil
}

func (fixtureDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	var execution fixtureExecution
	if err := json.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

type fixtureExecution struct {
	Value string `json:"value"`
	Done  bool   `json:"done"`
}

func (f *fixtureExecution) Step(context.Context, []agent.Signal) (agent.Transition, error) {
	if f.Done {
		return agent.Transition{}, agent.ErrInvalidExecutionState
	}
	f.Done = true
	output, err := agent.EncodeOutput(fixtureOutput{Value: f.Value})
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(0, output)
}

func (f *fixtureExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(f)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("test.trajectory", payload)
}

type rejectingDispatcher struct{}

func (rejectingDispatcher) Dispatch(
	context.Context,
	agent.EffectRequest,
	agent.DeltaEmitter,
) (agent.Settlement, error) {
	return agent.Settlement{}, errors.New("test dispatcher received an unexpected effect")
}

func (rejectingDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

type fixtureInteractionClient struct{ calls atomic.Uint32 }

func (f *fixtureInteractionClient) Call(context.Context, *chat.Request) (*chat.Response, error) {
	if f.calls.Add(1) == 1 {
		message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
			ID: "weather-call", Name: "weather", Arguments: `{"city":"Paris"}`,
		}))
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonToolCalls,
		}}, nil
	}
	message := chat.NewAssistantMessage(chat.NewTextPart("sunny"))
	return &chat.Response{Output: &chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}}, nil
}

func (*fixtureInteractionClient) Stream(
	context.Context,
	*chat.Request,
) iter.Seq2[*chat.ResponseDelta, error] {
	return func(func(*chat.ResponseDelta, error) bool) {}
}

type fixtureWeatherTool struct{ failure error }

func (fixtureWeatherTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name: "weather", Description: "Return deterministic fixture weather.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"city":{"type":"string"}},
			"required":["city"],
			"additionalProperties":false
		}`),
	}
}

func (f fixtureWeatherTool) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	if f.failure != nil {
		return chat.ToolOutput{}, f.failure
	}
	return chat.NewTextToolOutput("sunny"), nil
}

func runTrajectory(t *testing.T) trajectory.Trajectory {
	t.Helper()
	inputSchema, err := agent.SchemaFor[fixtureInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[fixtureOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "test.trajectory", Description: "Complete one deterministic trajectory.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: fixtureDefinition{descriptor: descriptor}, Dispatcher: rejectingDispatcher{},
		ImplementationDigest: agent.ComputeDigest([]byte("trajectory-test-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("trajectory-test-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &trajectory.Recorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{recorder}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(t.Context())) })
	input, err := agent.EncodeInput(fixtureInput{Value: "done"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	process, ok := engine.Process(result.ProcessID())
	if !ok {
		t.Fatal("root is missing")
	}
	recorded, err := recorder.Take(t.Context(), process, &trajectory.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

type trajectoryDeploymentResolver map[agent.DeploymentRef]agent.Deployment

func (t trajectoryDeploymentResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := t[reference]
	if !found {
		return agent.Deployment{}, errors.New("trajectory Tool deployment is not bound")
	}
	return deployment, nil
}

func rawOutputProjection(output agent.Output) (json.RawMessage, error) { return output.JSON(), nil }
