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
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestConcurrentToolsRespectLimitAndCommitInModelOrder(t *testing.T) {
	started := make(chan string, 3)
	var active atomic.Int32
	var maximum atomic.Int32
	tools := make([]tool.Tool, 0, 3)
	releases := make(map[string]*toolRelease)
	for _, name := range []string{"first", "second", "third"} {
		release := newToolRelease()
		releases[name] = release
		t.Cleanup(release.Release)
		tools = append(tools, &scheduledTool{
			name: name,
			call: func(context.Context, string) (string, error) {
				current := active.Add(1)
				defer active.Add(-1)
				for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
				}
				started <- name
				<-release.done
				return name + " result", nil
			},
		})
	}
	model := &orderedBatchModel{names: []string{"first", "second", "third"}}
	process, engine := startConcurrentInteraction(t, model, tools, 2)

	firstStarted := <-started
	secondStarted := <-started
	if firstStarted == secondStarted {
		t.Fatalf("started the same Tool twice: %q", firstStarted)
	}
	select {
	case thirdStarted := <-started:
		t.Fatalf("third Tool %q exceeded the concurrency limit", thirdStarted)
	default:
	}

	releases[firstStarted].Release()
	thirdStarted := <-started
	releases[secondStarted].Release()
	releases[thirdStarted].Release()
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Status())
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrent calls = %d, want 2", maximum.Load())
	}
}

func TestConcurrentToolsWithSameKeyDoNotOverlap(t *testing.T) {
	started := make(chan string, 2)
	firstRelease := newToolRelease()
	t.Cleanup(firstRelease.Release)
	first := &scheduledTool{
		name: "first", key: "resource:shared",
		call: func(context.Context, string) (string, error) {
			started <- "first"
			<-firstRelease.done
			return "first result", nil
		},
	}
	second := &scheduledTool{
		name: "second", key: "resource:shared",
		call: func(context.Context, string) (string, error) {
			started <- "second"
			return "second result", nil
		},
	}
	process, engine := startConcurrentInteraction(
		t,
		&orderedBatchModel{names: []string{"first", "second"}},
		[]tool.Tool{first, second},
		2,
	)
	if got := <-started; got != "first" {
		t.Fatalf("first started Tool = %q", got)
	}
	select {
	case got := <-started:
		t.Fatalf("same-key Tool %q overlapped", got)
	default:
	}
	firstRelease.Release()
	if got := <-started; got != "second" {
		t.Fatalf("second started Tool = %q", got)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Status())
	}
}

func TestZeroToolConcurrencyLimitKeepsDeclaredToolsSerial(t *testing.T) {
	started := make(chan string, 2)
	firstRelease := newToolRelease()
	t.Cleanup(firstRelease.Release)
	first := &scheduledTool{
		name: "first",
		call: func(context.Context, string) (string, error) {
			started <- "first"
			<-firstRelease.done
			return "first result", nil
		},
	}
	second := &scheduledTool{
		name: "second",
		call: func(context.Context, string) (string, error) {
			started <- "second"
			return "second result", nil
		},
	}
	process, engine := startConcurrentInteraction(
		t,
		&orderedBatchModel{names: []string{"first", "second"}},
		[]tool.Tool{first, second},
		0,
	)
	if got := <-started; got != "first" {
		t.Fatalf("first started Tool = %q", got)
	}
	select {
	case got := <-started:
		t.Fatalf("zero-limit Tool %q overlapped", got)
	default:
	}
	firstRelease.Release()
	if got := <-started; got != "second" {
		t.Fatalf("second started Tool = %q", got)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Status())
	}
}

func TestUndeclaredToolIsAnExclusiveBatchBarrier(t *testing.T) {
	started := make(chan string, 3)
	firstRelease := newToolRelease()
	secondRelease := newToolRelease()
	t.Cleanup(firstRelease.Release)
	t.Cleanup(secondRelease.Release)
	first := &scheduledTool{
		name: "first",
		call: func(context.Context, string) (string, error) {
			started <- "first"
			<-firstRelease.done
			return "first result", nil
		},
	}
	second := &exclusiveTool{
		name: "exclusive",
		call: func(context.Context, string) (string, error) {
			started <- "exclusive"
			<-secondRelease.done
			return "exclusive result", nil
		},
	}
	third := &scheduledTool{
		name: "third",
		call: func(context.Context, string) (string, error) {
			started <- "third"
			return "third result", nil
		},
	}
	process, engine := startConcurrentInteraction(
		t,
		&orderedBatchModel{names: []string{"first", "exclusive", "third"}},
		[]tool.Tool{first, second, third},
		3,
	)
	if got := <-started; got != "first" {
		t.Fatalf("first started Tool = %q", got)
	}
	select {
	case got := <-started:
		t.Fatalf("Tool %q crossed the exclusive barrier", got)
	default:
	}
	firstRelease.Release()
	if got := <-started; got != "exclusive" {
		t.Fatalf("second started Tool = %q", got)
	}
	select {
	case got := <-started:
		t.Fatalf("Tool %q overlapped the exclusive Tool", got)
	default:
	}
	secondRelease.Release()
	if got := <-started; got != "third" {
		t.Fatalf("third started Tool = %q", got)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Status())
	}
}

func TestDefinitionRejectsNegativeToolConcurrencyLimit(t *testing.T) {
	_, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.invalid_concurrency", Description: "Reject an invalid Tool concurrency limit.", MaxModelCalls: 1, MaxConcurrentToolCalls: -1,
	})
	if !errors.Is(err, interaction.ErrInvalidDefinitionConfig) {
		t.Fatalf("error=%v", err)
	}
}

func TestConcurrentToolInputWaitPreservesCompletedSibling(t *testing.T) {
	var siblingCalls atomic.Int32
	var requestingCalls atomic.Int32
	requesting := &scheduledTool{name: "requesting", call: func(ctx context.Context, _ string) (string, error) {
		requestingCalls.Add(1)
		if _, resumed := interaction.ToolInputContinuationFromContext(ctx); resumed {
			return "requesting result", nil
		}
		return "", interaction.RequireToolInput(json.RawMessage(`"continue?"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{"stage":1}`))
	}}
	sibling := &scheduledTool{name: "sibling", call: func(context.Context, string) (string, error) {
		siblingCalls.Add(1)
		return "sibling result", nil
	}}
	process, engine := startConcurrentInteraction(t, &orderedBatchModel{names: []string{"requesting", "sibling"}}, []tool.Tool{requesting, sibling}, 2)
	_, pending := captureToolInput(t, engine, process)
	toolProcess := pendingToolProcess(t, engine, pending)
	if unknown := inspectProcessSnapshot(t, engine, toolProcess).UnknownEffectIDs(); len(unknown) != 0 {
		t.Fatalf("waiting Tool unknown Effects=%v", unknown)
	}
	id, err := agent.ParseSignalID("signal:concurrent-tool-answer")
	if err != nil {
		t.Fatal(err)
	}
	response, err := pending.ResponseSignal(id, json.RawMessage(`true`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliverErr := toolProcess.DeliverSignals(t.Context(), response); deliverErr != nil || !accepted {
		t.Fatalf("answer=%t %v", accepted, deliverErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Status(), err)
	}
	if requestingCalls.Load() != 2 || siblingCalls.Load() != 1 {
		t.Fatalf("requesting/sibling calls=%d/%d", requestingCalls.Load(), siblingCalls.Load())
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type scheduledTool struct {
	name string
	key  string
	call func(context.Context, string) (string, error)
}

func (s *scheduledTool) Definition() chat.ToolDefinition {
	return concurrencyToolDefinition(s.name)
}

func (s *scheduledTool) Call(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	result, err := s.call(ctx, string(invocation.Arguments()))
	return chat.NewTextToolOutput(result), err
}

func (s *scheduledTool) ConcurrencyKey(tool.Invocation) (string, bool) { return s.key, true }

type exclusiveTool struct {
	name string
	call func(context.Context, string) (string, error)
}

func (e *exclusiveTool) Definition() chat.ToolDefinition {
	return concurrencyToolDefinition(e.name)
}

func (e *exclusiveTool) Call(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	result, err := e.call(ctx, string(invocation.Arguments()))
	return chat.NewTextToolOutput(result), err
}

type toolRelease struct {
	done chan struct{}
	once sync.Once
}

func newToolRelease() *toolRelease { return &toolRelease{done: make(chan struct{})} }

func (t *toolRelease) Release() { t.once.Do(func() { close(t.done) }) }

type orderedBatchModel struct {
	mu    sync.Mutex
	calls int
	names []string
}

func (o *orderedBatchModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.calls == 1 {
		calls := make([]chat.ToolCall, len(o.names))
		for index, name := range o.names {
			calls[index] = chat.ToolCall{ID: "call_" + name, Name: name, Arguments: `{}`}
		}
		return toolBatchResponse(calls...), nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != chat.RoleTool || len(last.Parts) != len(o.names) {
		return nil, errors.New("model did not receive the complete ordered Tool batch")
	}
	for index, name := range o.names {
		result := last.Parts[index].ToolResult
		resultText, textOK := toolResultText(result)
		if result == nil || !textOK || result.ID != "call_"+name || result.Name != name || resultText != name+" result" {
			return nil, errors.New("Tool results did not preserve model call order")
		}
	}
	return textResponse("done"), nil
}

func startConcurrentInteraction(
	t *testing.T,
	model chat.Model,
	tools []tool.Tool,
	maxConcurrent int,
) (*agent.Process, *agent.Engine) {
	t.Helper()
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	deployment := configuredInteraction(t, interaction.DefinitionConfig{
		Name: "interaction.concurrent", Description: "Verify bounded Tool concurrency.", MaxModelCalls: 3, MaxConcurrentToolCalls: maxConcurrent,
	}, interaction.DispatcherConfig{Model: client}, interaction.ToolSetConfig{Tools: tools})
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment.Deployment, interactionInput(t, "run tools"))
	if err != nil {
		_ = engine.Close(context.WithoutCancel(t.Context()))
		t.Fatal(err)
	}
	return process, engine
}

func concurrencyToolDefinition(name string) chat.ToolDefinition {
	return chat.ToolDefinition{
		Name: name, Description: "A deterministic concurrency contract test Tool.",
		InputSchema: []byte(`{"type":"object","additionalProperties":false}`),
	}
}

func toolBatchResponse(calls ...chat.ToolCall) *chat.Response {
	parts := make([]chat.Part, len(calls))
	for index := range calls {
		call := calls[index]
		parts[index] = chat.NewToolCallPart(call)
	}
	return &chat.Response{Output: &chat.Output{
		Message: new(chat.NewAssistantMessage(parts...)), FinishReason: chat.FinishReasonToolCalls,
	}}
}
