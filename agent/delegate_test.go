package agent_test

import (
	startrand "crypto/rand"

	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type delegateRequest struct {
	Value string `json:"value"`
}

type delegateResponse struct {
	Value string `json:"value"`
}

func TestManagedDelegatePreservesMixedToolCallOrder(t *testing.T) {
	child := delegateWorkflow(t, "interaction.delegate_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse{Value: strings.ToUpper(input.Value)}, nil
	})
	budget := run.Budget{Steps: run.NewQuota(50), Effects: run.NewQuota(50), Signals: run.NewQuota(50)}
	capability, _ := run.ParseCapability("worker.text")
	capabilities, _ := run.NewCapabilitySet(capability)
	delegate, err := agent.NewDelegate(agent.DelegateConfig{
		Name: "delegate_uppercase", Description: "Delegate text that must be converted to uppercase.",
		Program: child, Budget: budget, Capabilities: capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	type echoInput struct {
		Value string `json:"value"`
	}
	echo, err := tool.NewFunc(tool.FuncConfig{
		Name: "echo", Description: "Return the supplied text without changing it.",
	}, func(_ context.Context, input echoInput) (string, error) { return input.Value, nil })
	if err != nil {
		t.Fatal(err)
	}
	model := &mixedDelegateModel{}
	root := delegateInteraction(t, model, []tool.Tool{echo}, []agent.Delegate{delegate})
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(),
		Capabilities: capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(context.Background(), engine, root.Program, interactionInput(t, "mix Tool and Delegate calls"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination().Status() != run.StatusCompleted || model.Calls() != 2 {
		t.Fatalf("result status = %s, termination = %#v, model calls = %d", result.Termination().Status(), result.Termination(), model.Calls())
	}
	erased, _ := result.Termination().Output()
	output, err := erased.Decode[agent.Output]()
	if err != nil || output.ModelResponse == nil || output.ModelResponse.Text() != "mixed batch settled" {
		t.Fatalf("output = %#v, error = %v", output, err)
	}
	tree, err := engine.CaptureTree(context.Background(), result.ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tree.ProcessSnapshots()); got != 5 {
		t.Fatalf("Process tree size = %d, want 5", got)
	}
	for _, snapshot := range tree.ProcessSnapshots() {
		if snapshot.ProgramRef() == child.ProgramRef() &&
			(snapshot.Budget() != budget || !snapshot.Capabilities().Allows(capabilities)) {
			t.Fatalf("child allocation = %#v, %#v", snapshot.Budget(), snapshot.Capabilities())
		}
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestDelegateRejectsNonObjectInputAndToolNameCollision(t *testing.T) {
	primitive := delegateWorkflow(t, "interaction.primitive_worker", func(_ context.Context, value int) (int, error) {
		return value, nil
	})
	budget := run.Budget{Steps: run.NewQuota(10), Effects: run.NewQuota(10), Signals: run.NewQuota(10)}
	if _, err := agent.NewDelegate(agent.DelegateConfig{
		Name: "primitive", Description: "Delegate one primitive value.",
		Program: primitive, Budget: budget,
	}); !errors.Is(err, agent.ErrInvalidDelegate) {
		t.Fatalf("primitive Delegate error = %v", err)
	}

	child := delegateWorkflow(t, "interaction.collision_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse(input), nil
	})
	for name, config := range map[string]agent.DelegateConfig{
		"blank description": {
			Name: "invalid_blank", Program: child, Budget: budget,
		},
		"untrimmed description": {
			Name: "invalid_description", Description: " untrimmed", Program: child, Budget: budget,
		},
		"invalid name": {
			Name: "invalid/name", Description: "Use an invalid model capability name.", Program: child, Budget: budget,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := agent.NewDelegate(config); !errors.Is(err, agent.ErrInvalidDelegate) {
				t.Fatalf("NewDelegate error = %v", err)
			}
		})
	}
	delegate, err := agent.NewDelegate(agent.DelegateConfig{
		Name: "same_name", Description: "Delegate a value to the exact worker.",
		Program: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, newDefinitionErr := agent.NewDefinition(agent.DefinitionConfig{
		Name: "interaction.duplicate_delegates", Description: "Reject duplicate managed Delegate names.",
		MaxModelCalls: run.NewQuota(2),
		Delegates:     []agent.Delegate{delegate, delegate},
	}); !errors.Is(newDefinitionErr, agent.ErrInvalidDefinitionConfig) {
		t.Fatalf("duplicate Delegate error = %v", newDefinitionErr)
	}
	colliding, err := tool.NewFunc(tool.FuncConfig{
		Name: "same_name", Description: "Return a value locally.",
	}, func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse(input), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	toolSet := testToolSet(t, agent.ToolSetConfig{Tools: []tool.Tool{colliding}})
	if _, err := agent.NewDefinition(agent.DefinitionConfig{
		Name: "interaction.collision", Description: "Reject duplicate model-visible authority.", MaxModelCalls: run.NewQuota(2),
		Delegates: []agent.Delegate{delegate}, Tools: toolSet, ToolBudget: run.Budget{Steps: run.NewQuota(8), Effects: run.NewQuota(4), Signals: run.NewQuota(8)},
	}); !errors.Is(err, agent.ErrInvalidDefinitionConfig) {
		t.Fatalf("name collision error=%v", err)
	}
}

func TestDelegateDescriptionContract(t *testing.T) {
	child := delegateWorkflow(t, "interaction.description_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse(input), nil
	})
	for _, test := range []struct {
		name        string
		description string
		valid       bool
	}{
		{name: "unicode", description: "Delegate multilingual work: 你好, café.", valid: true},
		{name: "ascii byte limit", description: strings.Repeat("a", 4096), valid: true},
		{name: "multibyte byte limit", description: strings.Repeat("界", 1365) + "a", valid: true},
		{name: "invalid UTF-8", description: string([]byte{0xff})},
		{name: "truncated UTF-8", description: string([]byte{0xe7, 0x95})},
		{name: "above ascii byte limit", description: strings.Repeat("a", 4097)},
		{name: "above multibyte byte limit", description: strings.Repeat("界", 1365) + "ab"},
	} {
		t.Run(test.name, func(t *testing.T) {
			delegate, err := agent.NewDelegate(agent.DelegateConfig{
				Name: "worker", Description: test.description, Program: child,
			})
			if test.valid {
				if err != nil || !delegate.Valid() {
					t.Fatalf("NewDelegate with %d description bytes: valid=%v, error=%v", len(test.description), delegate.Valid(), err)
				}
				return
			}
			if !errors.Is(err, agent.ErrInvalidDelegate) || delegate.Valid() {
				t.Fatalf("NewDelegate with %d description bytes: valid=%v, error=%v", len(test.description), delegate.Valid(), err)
			}
		})
	}
}

func TestManagedDelegateReturnsArgumentAndStartFailuresToModel(t *testing.T) {
	child := delegateWorkflow(t, "interaction.unavailable_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse(input), nil
	})
	budget := run.Budget{Steps: run.NewQuota(10), Effects: run.NewQuota(10), Signals: run.NewQuota(10)}
	delegate, err := agent.NewDelegate(agent.DelegateConfig{
		Name: "delegate_unavailable", Description: "Delegate work to an exact worker that may be unavailable.",
		Program: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &delegateFailureModel{}
	root := delegateInteraction(t, model, nil, []agent.Delegate{delegate})
	engine, err := run.New(t.Context(), run.Config{
		Store: run.NewMemoryStore(),
		ProcessAdmitter: run.ProcessAdmitterFunc(func(_ context.Context, admission run.ProcessAdmission) error {
			if admission.Relation().IsRoot() {
				return nil
			}
			return errors.New("worker unavailable")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(context.Background(), engine, root.Program, interactionInput(t, "exercise Delegate failures"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination().Status() != run.StatusCompleted || model.Calls() != 3 {
		t.Fatalf("result = %#v, model calls = %d", result.Termination(), model.Calls())
	}
	tree, err := engine.CaptureTree(context.Background(), result.ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != 1 {
		t.Fatalf("failed Delegate starts created %d Processes", len(tree.ProcessSnapshots()))
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingManagedDelegateTreeRestoresWithoutRestartingChild(t *testing.T) {
	fixture := newDelegateRestoreFixture(t)
	tree, childID := captureWaitingDelegateTree(t, fixture)
	completeRestoredDelegateTree(t, fixture, tree, childID)
}

type delegateRestoreFixture struct {
	store *run.MemoryStore
	child run.Program[run.Payload, run.Payload]
	root  run.Program[run.Payload, run.Payload]
	model *restorableDelegateModel
}

func newDelegateRestoreFixture(t *testing.T) delegateRestoreFixture {
	t.Helper()
	child := pausingDelegateDeployment(t)
	budget := run.Budget{Steps: run.NewQuota(20), Effects: run.NewQuota(20), Signals: run.NewQuota(20)}
	delegate, err := agent.NewDelegate(agent.DelegateConfig{
		Name: "delegate_paused", Description: "Delegate work that may pause before producing its result.",
		Program: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &restorableDelegateModel{}
	rootDeployment := delegateInteraction(t, model, nil, []agent.Delegate{delegate})
	return delegateRestoreFixture{store: run.NewMemoryStore(), child: child, root: rootDeployment.Program, model: model}
}

func captureWaitingDelegateTree(
	t *testing.T,
	fixture delegateRestoreFixture,
) (run.TreeSnapshot, run.ProcessID) {
	t.Helper()
	engine, _ := run.New(t.Context(), run.Config{Store: fixture.store})
	root, err := engine.Start(context.Background(), fixture.root, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "pause and restore")})
	if err != nil {
		t.Fatal(err)
	}
	tree, childID := awaitWaitingDelegateTree(t, engine, root.Relation().ProcessID())
	pending, err := agent.PendingToolInputs(tree)
	if err != nil || len(pending) != 0 {
		t.Fatalf("Delegate tree exposes Tool input: %v %v", pending, err)
	}
	rootSnapshot := rootProcessSnapshot(tree)
	assertDelegateAttribution(t, rootSnapshot, childID)
	t.Cleanup(func() { retireTestWriter(t, engine, root) })
	return tree, childID
}

func rootProcessSnapshot(tree run.TreeSnapshot) run.ProcessSnapshot {
	for _, snapshot := range tree.ProcessSnapshots() {
		if snapshot.Relation().IsRoot() {
			return snapshot
		}
	}
	return run.ProcessSnapshot{}
}

func assertDelegateAttribution(t *testing.T, rootSnapshot run.ProcessSnapshot, childID run.ProcessID) {
	t.Helper()
	attributions, found, err := agent.DelegateAttributions(rootSnapshot)
	if err != nil || !found || len(attributions) != 1 {
		t.Fatalf("delegate attributions = %#v, found = %t, error = %v", attributions, found, err)
	}
	attribution := attributions[0]
	reference, attributed := attribution.Reference()
	if !attributed || reference.ProcessID() != rootSnapshot.Relation().ProcessID() || reference.ModelCallSequence() != 1 || reference.ToolCallIndex() != 0 {
		t.Fatalf("delegate attribution reference=%v, present=%v", reference, attributed)
	}
	if !attribution.Valid() || attribution.ModelCallSequence() != 1 ||
		attribution.ToolCallIndex() != 0 || attribution.ToolCall().ID != "call_paused" ||
		attribution.ChildKey().String() == "" || attribution.ProcessID() != childID {
		t.Fatalf("delegate attribution = %#v", attribution)
	}
}

func completeRestoredDelegateTree(
	t *testing.T,
	fixture delegateRestoreFixture,
	tree run.TreeSnapshot,
	childID run.ProcessID,
) {
	t.Helper()
	restoredEngine, _ := run.New(t.Context(), run.Config{Store: fixture.store})
	restoredRoot, err := restoredEngine.Recover(context.Background(), fixture.root, tree)
	if err != nil {
		t.Fatal(err)
	}
	restoredChild, found := restoredEngine.Process(childID)
	if !found {
		t.Fatalf("restored child %s was not registered", childID)
	}
	if inspectProcessSnapshot(t, restoredEngine, restoredChild).Status() != run.StatusPaused {
		t.Fatalf("restored child %s status = %s", childID, inspectProcessSnapshot(t, restoredEngine, restoredChild).Status())
	}
	if resumeErr := restoredChild.Resume(context.Background()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	result, err := restoredRoot.Await(context.Background())
	if err != nil || result.Termination().Status() != run.StatusCompleted || fixture.model.Calls() != 2 {
		t.Fatalf("restored result = %#v, error = %v, model calls = %d", result.Termination(), err, fixture.model.Calls())
	}
	finalTree, err := restoredEngine.CaptureTree(context.Background(), restoredRoot.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if len(finalTree.ProcessSnapshots()) != 2 {
		t.Fatalf("restored tree created duplicate children: %d Processes", len(finalTree.ProcessSnapshots()))
	}
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type mixedDelegateModel struct {
	mu    sync.Mutex
	calls int
}

type delegateFailureModel struct {
	mu    sync.Mutex
	calls int
}

type restorableDelegateModel struct {
	mu    sync.Mutex
	calls int
}

func (r *restorableDelegateModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 {
		return toolCallResponse(chat.ToolCall{
			ID: "call_paused", Name: "delegate_paused", Arguments: `{"value":"restored"}`,
		}), nil
	}
	if len(request.Messages) != 3 || len(request.Messages[2].Parts) != 1 {
		return nil, errors.New("restored Delegate result is absent from WorkingContext")
	}
	result := request.Messages[2].Parts[0].ToolResult
	resultText, textOK := toolResultText(result)
	if result == nil || !textOK || result.IsError || resultText != `{"value":"restored"}` {
		return nil, fmt.Errorf("restored Delegate result = %#v", result)
	}
	return textResponse("restored child settled"), nil
}

func (r *restorableDelegateModel) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (d *delegateFailureModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.calls == 1 {
		message := chat.NewAssistantMessage(
			chat.NewToolCallPart(chat.ToolCall{
				ID: "call_invalid", Name: "delegate_unavailable", Arguments: `{"unknown":true}`,
			}),
			chat.NewToolCallPart(chat.ToolCall{
				ID: "call_unavailable", Name: "delegate_unavailable", Arguments: `{"value":"valid"}`,
			}),
		)
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonToolCalls,
		}}, nil
	}
	if d.calls == 2 {
		if len(request.Messages) != 3 || len(request.Messages[2].Parts) != 2 {
			return nil, errors.New("invalid Delegate batch did not return both rejected proposals")
		}
		for _, part := range request.Messages[2].Parts {
			result := part.ToolResult
			text, ok := toolResultText(result)
			if result == nil || !ok || !result.IsError || !strings.Contains(text, "batch was not executed") || !strings.Contains(text, "input contract") {
				return nil, fmt.Errorf("invalid batch reply: %v", result)
			}
		}
		return toolCallResponse(chat.ToolCall{ID: "retry_unavailable", Name: "delegate_unavailable", Arguments: `{"value":"valid"}`}), nil
	}
	final := request.Messages[len(request.Messages)-1]
	if len(final.Parts) != 1 {
		return nil, errors.New("missing child admission feedback")
	}
	result := final.Parts[0].ToolResult
	text, ok := toolResultText(result)
	if result == nil || !ok || !result.IsError || !strings.Contains(text, "engine.child.admission.rejected") {
		return nil, fmt.Errorf("unexpected retry result: %v", result)
	}
	return textResponse("failures observed"), nil
}

func (d *delegateFailureModel) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (m *mixedDelegateModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if len(request.Tools) != 2 || request.Tools[0].Name != "echo" ||
		request.Tools[1].Name != "delegate_uppercase" {
		return nil, fmt.Errorf("model capability manifest = %#v", request.Tools)
	}
	if m.calls == 1 {
		message := chat.NewAssistantMessage(
			chat.NewToolCallPart(chat.ToolCall{ID: "call_before", Name: "echo", Arguments: `{"value":"before"}`}),
			chat.NewToolCallPart(chat.ToolCall{ID: "call_worker_1", Name: "delegate_uppercase", Arguments: `{"value":"first"}`}),
			chat.NewToolCallPart(chat.ToolCall{ID: "call_worker_2", Name: "delegate_uppercase", Arguments: `{"value":"second"}`}),
			chat.NewToolCallPart(chat.ToolCall{ID: "call_after", Name: "echo", Arguments: `{"value":"after"}`}),
		)
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonToolCalls,
		}}, nil
	}
	if len(request.Messages) != 3 || request.Messages[2].Role != chat.RoleTool ||
		len(request.Messages[2].Parts) != 4 {
		return nil, errors.New("mixed results were not appended as one ordered Tool message")
	}
	want := []struct {
		id, name, result string
	}{
		{id: "call_before", name: "echo", result: "before"},
		{id: "call_worker_1", name: "delegate_uppercase", result: `{"value":"FIRST"}`},
		{id: "call_worker_2", name: "delegate_uppercase", result: `{"value":"SECOND"}`},
		{id: "call_after", name: "echo", result: "after"},
	}
	for index, expected := range want {
		result := request.Messages[2].Parts[index].ToolResult
		resultText, textOK := toolResultText(result)
		if result == nil || result.ID != expected.id || result.Name != expected.name ||
			!textOK || resultText != expected.result || result.IsError {
			return nil, fmt.Errorf("Tool result %d = %#v", index, result)
		}
	}
	return textResponse("mixed batch settled"), nil
}

func (m *mixedDelegateModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func delegateInteraction(t *testing.T, model chat.Model, tools []tool.Tool, delegates []agent.Delegate) interactionDeployment {
	return delegateInteractionWithValidator(t, model, tools, delegates, nil, 3)
}

func delegateInteractionWithValidator(t *testing.T, model chat.Model, tools []tool.Tool, delegates []agent.Delegate, validator agent.CompletionValidator, maxModelCalls uint32) interactionDeployment {
	t.Helper()
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return configuredInteraction(t, agent.DefinitionConfig{
		Name: "interaction.delegate_root", Description: "Exercise exact managed worker delegation.", MaxModelCalls: run.NewQuota(uint64(maxModelCalls)), Delegates: delegates, CompletionValidator: validator,
	}, agent.DispatcherConfig{Model: client}, agent.ToolSetConfig{Tools: tools})
}

func delegateWorkflow[I, O any](t *testing.T, name string, transform workflow.TransformFunc[I, O]) run.Program[run.Payload, run.Payload] {
	t.Helper()
	stage, err := workflow.Transform("work", transform)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: name, Description: "Run one deterministic delegated worker operation.",
		Stages: []workflow.Stage{stage},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           definition,
		ImplementationDigest: run.ComputeDigest([]byte(name + "-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte(name + "-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

type pausingDelegateDefinition struct {
	descriptor run.Descriptor
}

type pausingDelegateExecution struct {
	Input delegateRequest `json:"input"`
	Ready bool            `json:"ready"`
}

func pausingDelegateDeployment(t *testing.T) run.Program[run.Payload, run.Payload] {
	t.Helper()
	inputSchema, _ := jsonschema.For[delegateRequest]()
	outputSchema, _ := jsonschema.For[delegateResponse]()
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: "interaction.pausing_delegate_worker", Description: "Pause once and then return the delegated value.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &pausingDelegateDefinition{descriptor: descriptor}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: pausingDelegateDispatcher{},
		ImplementationDigest: run.ComputeDigest([]byte("pausing-delegate-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte("pausing-delegate-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (p *pausingDelegateDefinition) Descriptor() run.Descriptor {
	return p.descriptor
}

func (*pausingDelegateDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (*pausingDelegateDefinition) Start(input run.Payload) (run.Execution, error) {
	decoded, err := input.Decode[delegateRequest]()
	if err != nil {
		return nil, err
	}
	return &pausingDelegateExecution{Input: decoded}, nil
}

func (*pausingDelegateDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if state.Kind() != "test.pausing_delegate" {
		return nil, errors.New("invalid pausing Delegate state")
	}
	var execution pausingDelegateExecution
	if err := jsonv2.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

func (p *pausingDelegateExecution) Step(context.Context, []run.Signal) (run.Transition, error) {
	if !p.Ready {
		p.Ready = true
		return run.Pause(0, "test worker waits at a recoverable boundary")
	}
	output, err := run.EncodePayload(delegateResponse{Value: p.Input.Value})
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(0, output)
}

func (p *pausingDelegateExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := jsonv2.Marshal(p)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState("test.pausing_delegate", payload)
}

type pausingDelegateDispatcher struct{}

func (pausingDelegateDispatcher) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	return run.Settlement{}, errors.New("pausing Delegate worker has no dispatcher Effects")
}

func (pausingDelegateDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func awaitWaitingDelegateTree(
	t *testing.T,
	engine *run.Runtime,
	rootID run.ProcessID,
) (run.TreeSnapshot, run.ProcessID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		tree, err := engine.CaptureTree(ctx, rootID)
		if err != nil {
			t.Fatalf("capture Delegate tree: %v", err)
		}
		var rootWaiting bool
		var childID run.ProcessID
		for _, snapshot := range tree.ProcessSnapshots() {
			switch snapshot.Relation().Depth() {
			case 0:
				rootWaiting = snapshot.Status() == run.StatusWaiting
			case 1:
				if snapshot.Status() == run.StatusPaused {
					childID = snapshot.Relation().ProcessID()
				}
			}
		}
		if rootWaiting && childID.Valid() {
			return tree, childID
		}
		runtime.Gosched()
	}
}
