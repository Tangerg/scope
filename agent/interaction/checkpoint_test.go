package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolCheckpointRestoresWithoutRepeatingSettledPrefix(t *testing.T) {
	fixture := newCheckpointFixture(t)
	tree, pending := captureWaitingCheckpoint(t, fixture.deployment)
	restored, result := restoreWaitingCheckpoint(t, fixture.deployment, tree, pending, fixture.waiting)
	assertCheckpointResult(t, result, fixture)
	assertFinishedCheckpointRejectsStaleSignal(t, restored, pending)
}

type checkpointFixture struct {
	prefixCalls *atomic.Int32
	waiting     *inputRequestTool
	model       *checkpointModel
	deployment  agent.Deployment
}

func newCheckpointFixture(t *testing.T) checkpointFixture {
	t.Helper()
	var prefixCalls atomic.Int32
	prefix, err := tool.NewFunc(tool.FuncConfig{
		Name:        "prefix",
		Description: "Complete before the next tool requests input.",
	}, func(context.Context, struct{}) (string, error) {
		prefixCalls.Add(1)
		return "prefix-complete", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting := newInputRequestTool()
	t.Cleanup(waiting.Release)
	model := &checkpointModel{}
	deployment := newDeployment(t, model, []tool.Tool{prefix, waiting}, 3)
	return checkpointFixture{
		prefixCalls: &prefixCalls,
		waiting:     waiting,
		model:       model,
		deployment:  deployment,
	}
}

func captureWaitingCheckpoint(
	t *testing.T,
	deployment agent.Deployment,
) (agent.TreeSnapshot, interaction.PendingToolInput) {
	t.Helper()
	firstEngine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	process, err := firstEngine.Start(context.Background(), deployment, interactionInput(t, "checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, agent.StatusWaiting)
	waitID, ok := process.WaitID()
	if !ok {
		t.Fatal("waiting Process has no WaitID")
	}
	livePending, found, err := interaction.PendingToolInputFromProcess(context.Background(), process)
	if err != nil || !found || livePending.WaitID() != waitID {
		t.Fatalf("PendingToolInputFromProcess found = %t, WaitID = %s, error = %v", found, livePending.WaitID().String(), err)
	}
	snapshot, err := process.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := firstEngine.CaptureTree(context.Background(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	if killErr := process.Kill(context.Background(), "replace with restored Process"); killErr != nil {
		t.Fatal(killErr)
	}
	if _, awaitErr := process.Await(context.Background()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	if closeErr := firstEngine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	pending, found, err := interaction.PendingToolInputFromSnapshot(snapshot)
	if err != nil || !found {
		t.Fatalf("PendingToolInputFromSnapshot found = %t, error = %v", found, err)
	}
	if pending.WaitID() != waitID || string(pending.Prompt()) != `{"question":"What is your name?"}` {
		t.Fatalf("pending input = WaitID %s, prompt %s", pending.WaitID().String(), pending.Prompt())
	}
	return tree, pending
}

func restoreWaitingCheckpoint(
	t *testing.T,
	deployment agent.Deployment,
	tree agent.TreeSnapshot,
	pending interaction.PendingToolInput,
	waiting *inputRequestTool,
) (*agent.Process, agent.Result) {
	t.Helper()
	restoredEngine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	answer := validCheckpointAnswer(t, restored, pending)
	accepted, err := restored.DeliverSignals(context.Background(), answer)
	if err != nil || !accepted {
		t.Fatalf("DeliverSignals accepted = %t, error = %v", accepted, err)
	}
	<-waiting.continuationStarted
	accepted, err = restored.DeliverSignals(context.Background(), answer)
	if err != nil || accepted {
		t.Fatalf("duplicate DeliverSignals accepted = %t, error = %v", accepted, err)
	}
	waiting.Release()
	result, err := restored.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := restoredEngine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return restored, result
}

func validCheckpointAnswer(
	t *testing.T,
	restored *agent.Process,
	pending interaction.PendingToolInput,
) agent.SignalRequest {
	t.Helper()
	signalID, err := agent.ParseSignalID("signal:checkpoint-answer")
	if err != nil {
		t.Fatal(err)
	}
	invalidID, err := agent.ParseSignalID("signal:invalid-checkpoint-answer")
	if err != nil {
		t.Fatal(err)
	}
	if _, responseSignalErr := pending.ResponseSignal(invalidID, json.RawMessage(`42`)); !errors.Is(responseSignalErr, interaction.ErrInvalidToolInputRequest) {
		t.Fatalf("invalid response error = %v, want ErrInvalidToolInputRequest", responseSignalErr)
	}
	wrongWaitID, err := agent.ParseWaitID("wait:wrong")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := interaction.NewToolInputResponseSignal(invalidID, wrongWaitID, json.RawMessage(`"Ada"`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliverSignalErr := restored.DeliverSignals(context.Background(), wrong); accepted || !errors.Is(deliverSignalErr, agent.ErrSignalRejected) {
		t.Fatalf("wrong-wait delivery accepted = %t, error = %v", accepted, deliverSignalErr)
	}
	answer, err := pending.ResponseSignal(signalID, json.RawMessage(`"Ada"`))
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

func assertCheckpointResult(t *testing.T, result agent.Result, fixture checkpointFixture) {
	t.Helper()
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	if fixture.prefixCalls.Load() != 1 {
		t.Fatalf("settled prefix calls = %d, want 1", fixture.prefixCalls.Load())
	}
	if fixture.waiting.initialCalls.Load() != 1 || fixture.waiting.continuationCalls.Load() != 1 {
		t.Fatalf("input tool initial/continuation calls = %d/%d, want 1/1", fixture.waiting.initialCalls.Load(), fixture.waiting.continuationCalls.Load())
	}
	if fixture.model.Calls() != 2 {
		t.Fatalf("model calls = %d, want 2", fixture.model.Calls())
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.ModelResponse == nil || output.ModelResponse.Text() != "prefix-complete; hello Ada" {
		t.Fatalf("output = %#v", output)
	}
}

func assertFinishedCheckpointRejectsStaleSignal(
	t *testing.T,
	restored *agent.Process,
	pending interaction.PendingToolInput,
) {
	t.Helper()
	staleID, err := agent.ParseSignalID("signal:stale-checkpoint-answer")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := pending.ResponseSignal(staleID, json.RawMessage(`"Grace"`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := restored.DeliverSignals(context.Background(), stale); accepted || !errors.Is(err, agent.ErrProcessFinished) {
		t.Fatalf("stale delivery accepted = %t, error = %v", accepted, err)
	}
}

type inputRequestTool struct {
	initialCalls        atomic.Int32
	continuationCalls   atomic.Int32
	continuationStarted chan struct{}
	continuationRelease chan struct{}
	startedOnce         sync.Once
	releaseOnce         sync.Once
}

func newInputRequestTool() *inputRequestTool {
	return &inputRequestTool{
		continuationStarted: make(chan struct{}),
		continuationRelease: make(chan struct{}),
	}
}

func (*inputRequestTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        "ask_name",
		Description: "Ask for a name and continue with the validated answer.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
}

func (i *inputRequestTool) Call(ctx context.Context, _ tool.Invocation) (chat.ToolOutput, error) {
	continuation, resumed := interaction.ToolInputContinuationFromContext(ctx)
	if !resumed {
		i.initialCalls.Add(1)
		return chat.ToolOutput{}, interaction.RequireToolInput(
			json.RawMessage(`{"question":"What is your name?"}`),
			json.RawMessage(`{"type":"string","minLength":1}`),
			json.RawMessage(`{"stage":"awaiting_name"}`),
		)
	}
	i.continuationCalls.Add(1)
	i.startedOnce.Do(func() { close(i.continuationStarted) })
	<-i.continuationRelease
	var state struct {
		Stage string `json:"stage"`
	}
	if err := json.Unmarshal(continuation.State(), &state); err != nil || state.Stage != "awaiting_name" {
		return chat.ToolOutput{}, errors.New("invalid continuation state")
	}
	var name string
	if err := json.Unmarshal(continuation.Response(), &name); err != nil {
		return chat.ToolOutput{}, err
	}
	return chat.NewTextToolOutput("hello " + name), nil
}

func (i *inputRequestTool) Release() {
	i.releaseOnce.Do(func() { close(i.continuationRelease) })
}

type checkpointModel struct {
	mu    sync.Mutex
	calls int
}

func (c *checkpointModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return toolCallsResponse(
			chat.ToolCall{ID: "call_prefix", Name: "prefix", Arguments: `{}`},
			chat.ToolCall{ID: "call_name", Name: "ask_name", Arguments: `{}`},
		), nil
	}
	if len(request.Messages) != 3 || request.Messages[2].Role != chat.RoleTool || len(request.Messages[2].Parts) != 2 {
		return nil, errors.New("restored continuation lost ordered ToolResults")
	}
	first := request.Messages[2].Parts[0].ToolResult
	second := request.Messages[2].Parts[1].ToolResult
	firstText, firstOK := toolResultText(first)
	secondText, secondOK := toolResultText(second)
	if !firstOK || !secondOK || firstText != "prefix-complete" || secondText != "hello Ada" {
		return nil, errors.New("restored continuation contains incorrect ToolResults")
	}
	return textResponse("prefix-complete; hello Ada"), nil
}

func (c *checkpointModel) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func waitForStatus(t *testing.T, process *agent.Process, want agent.Status) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		snapshot, err := process.Snapshot(ctx)
		if err != nil {
			t.Fatalf("capture Process while waiting for %s: %v", want, err)
		}
		if snapshot.Status() == want {
			return
		}
		runtime.Gosched()
	}
}

func toolCallsResponse(calls ...chat.ToolCall) *chat.Response {
	parts := make([]chat.Part, len(calls))
	for index := range calls {
		parts[index] = chat.NewToolCallPart(calls[index])
	}
	message := chat.NewAssistantMessage(parts...)
	return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}
}
