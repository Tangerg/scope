package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestSteerDuringModelCallIsVisibleOnlyToNextModelCall(t *testing.T) {
	model := newSteeredModel()
	deployment := newDeployment(t, model, nil, 3)
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment, interactionInput(t, "initial"))
	if err != nil {
		t.Fatal(err)
	}
	<-model.firstStarted
	steerID, err := agent.ParseSignalID("signal:model-steer")
	if err != nil {
		t.Fatal(err)
	}
	steer, err := interaction.NewSteerSignal(
		steerID,
		chat.NewUserMessage(chat.NewTextPart("revise the draft")),
	)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := process.DeliverSignals(context.Background(), steer)
	if err != nil || !accepted {
		t.Fatalf("DeliverSignals accepted = %t, error = %v", accepted, err)
	}
	model.ReleaseFirst()
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if result.Status() != agent.StatusCompleted || model.Calls() != 2 {
		t.Fatalf("status = %s, model calls = %d", result.Status(), model.Calls())
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.ModelResponse == nil || output.ModelResponse.Text() != "revised" {
		t.Fatalf("output = %#v", output)
	}
}

func TestSteerDuringToolBatchWaitsForWholeBatchSettlement(t *testing.T) {
	blocking := newSteeredTool()
	t.Cleanup(blocking.Release)
	model := &toolSteerModel{}
	deployment := newDeployment(t, model, []tool.Tool{blocking}, 3)
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment, interactionInput(t, "initial"))
	if err != nil {
		t.Fatal(err)
	}
	<-blocking.started
	steerID, err := agent.ParseSignalID("signal:tool-steer")
	if err != nil {
		t.Fatal(err)
	}
	steer, err := interaction.NewSteerSignal(
		steerID,
		chat.NewUserMessage(chat.NewTextPart("include the settled tool result")),
	)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := process.DeliverSignals(context.Background(), steer)
	if err != nil || !accepted {
		t.Fatalf("DeliverSignals accepted = %t, error = %v", accepted, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("model calls before Tool settlement = %d, want 1", model.Calls())
	}
	blocking.Release()
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted || model.Calls() != 2 || blocking.calls.Load() != 1 {
		t.Fatalf("status = %s, model calls = %d, Tool calls = %d", result.Status(), model.Calls(), blocking.calls.Load())
	}
}

func TestSteerRequiresUserMessages(t *testing.T) {
	id, err := agent.ParseSignalID("signal:invalid-steer")
	if err != nil {
		t.Fatal(err)
	}
	_, err = interaction.NewSteerSignal(id, chat.NewAssistantMessage(chat.NewTextPart("not user input")))
	if !errors.Is(err, interaction.ErrInvalidSteer) {
		t.Fatalf("error = %v, want ErrInvalidSteer", err)
	}
}

type steeredModel struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	firstRelease chan struct{}
	releaseOnce  sync.Once
}

func newSteeredModel() *steeredModel {
	return &steeredModel{firstStarted: make(chan struct{}), firstRelease: make(chan struct{})}
}

func (s *steeredModel) Call(ctx context.Context, request *chat.Request) (*chat.Response, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	invocation, ok := interaction.ModelInvocationFromContext(ctx)
	if !ok {
		return nil, errors.New("model invocation attribution is missing")
	}
	appliedSteerSignalIDs := invocation.AppliedSteerSignalIDs()
	if call == 1 {
		if len(appliedSteerSignalIDs) != 0 {
			return nil, errors.New("initial model call reports applied steer input")
		}
		if len(request.Messages) != 1 {
			return nil, errors.New("steer reached the in-flight model request")
		}
		close(s.firstStarted)
		<-s.firstRelease
		return textResponse("draft"), nil
	}
	if len(appliedSteerSignalIDs) != 1 || appliedSteerSignalIDs[0].String() != "signal:model-steer" {
		return nil, errors.New("next model call has inaccurate steer Signal attribution")
	}
	appliedSteerSignalIDs[0] = agent.SignalID{}
	if invocation.AppliedSteerSignalIDs()[0].String() != "signal:model-steer" {
		return nil, errors.New("model invocation aliases steer Signal attribution")
	}
	if len(request.Messages) != 3 || request.Messages[1].Role != chat.RoleAssistant ||
		request.Messages[1].Text() != "draft" || request.Messages[2].Role != chat.RoleUser ||
		request.Messages[2].Text() != "revise the draft" {
		return nil, errors.New("steer was not appended at the next safe model boundary")
	}
	return textResponse("revised"), nil
}

func (s *steeredModel) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *steeredModel) ReleaseFirst() {
	s.releaseOnce.Do(func() { close(s.firstRelease) })
}

type steeredTool struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	calls       atomic.Int32
}

func newSteeredTool() *steeredTool {
	return &steeredTool{started: make(chan struct{}), release: make(chan struct{})}
}

func (*steeredTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        "settle",
		Description: "Settle only after the test releases the Tool batch.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
}

func (s *steeredTool) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	s.calls.Add(1)
	s.startedOnce.Do(func() { close(s.started) })
	<-s.release
	return chat.NewTextToolOutput("settled"), nil
}

func (s *steeredTool) Release() {
	s.releaseOnce.Do(func() { close(s.release) })
}

type toolSteerModel struct {
	mu    sync.Mutex
	calls int
}

func (t *toolSteerModel) Call(ctx context.Context, request *chat.Request) (*chat.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	invocation, ok := interaction.ModelInvocationFromContext(ctx)
	if !ok {
		return nil, errors.New("model invocation attribution is missing")
	}
	if t.calls == 1 {
		if len(invocation.AppliedSteerSignalIDs()) != 0 {
			return nil, errors.New("initial model call reports applied steer input")
		}
		return toolCallResponse(chat.ToolCall{ID: "call_settle", Name: "settle", Arguments: `{}`}), nil
	}
	appliedSteerSignalIDs := invocation.AppliedSteerSignalIDs()
	if len(appliedSteerSignalIDs) != 1 || appliedSteerSignalIDs[0].String() != "signal:tool-steer" {
		return nil, errors.New("post-Tool model call has inaccurate steer Signal attribution")
	}
	if len(request.Messages) != 4 || request.Messages[1].Role != chat.RoleAssistant ||
		request.Messages[2].Role != chat.RoleTool || request.Messages[3].Role != chat.RoleUser ||
		request.Messages[3].Text() != "include the settled tool result" {
		return nil, errors.New("steer became visible before the complete Tool batch boundary")
	}
	return textResponse("settled and steered"), nil
}

func (t *toolSteerModel) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}
