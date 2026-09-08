package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
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
	budget, err := agent.NewBudget(agent.BudgetConfig{Steps: 50, Effects: 50, Signals: 50})
	if err != nil {
		t.Fatal(err)
	}
	capability, _ := agent.ParseCapability("worker.text")
	capabilities, _ := agent.NewCapabilitySet(capability)
	delegate, err := interaction.NewDelegate(interaction.DelegateConfig{
		Name: "delegate_uppercase", Description: "Delegate text that must be converted to uppercase.",
		Deployment: child, Budget: budget, Capabilities: capabilities,
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
	root := delegateInteraction(t, model, []tool.Tool{echo}, []interaction.Delegate{delegate})
	engine, err := agent.NewEngine(agent.EngineConfig{
		DeploymentResolver: delegateResolver{child.DeploymentRef(): child}, Capabilities: capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), root, interactionInput(t, "mix Tool and Delegate calls"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted || model.Calls() != 2 {
		t.Fatalf("result status = %s, termination = %#v, model calls = %d", result.Status(), result.Termination(), model.Calls())
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil || output.ModelResponse == nil || output.ModelResponse.Text() != "mixed batch settled" {
		t.Fatalf("output = %#v, error = %v", output, err)
	}
	tree, err := engine.CaptureTree(context.Background(), result.ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tree.ProcessSnapshots()); got != 3 {
		t.Fatalf("Process tree size = %d, want 3", got)
	}
	for _, snapshot := range tree.ProcessSnapshots() {
		if snapshot.Relation().Depth() == 1 &&
			(snapshot.Budget() != budget || !snapshot.Capabilities().Allows(capabilities)) {
			t.Fatalf("child allocation = %#v, %#v", snapshot.Budget(), snapshot.Capabilities())
		}
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDelegateRejectsNonObjectInputAndToolNameCollision(t *testing.T) {
	primitive := delegateWorkflow(t, "interaction.primitive_worker", func(_ context.Context, value int) (int, error) {
		return value, nil
	})
	budget, _ := agent.NewBudget(agent.BudgetConfig{Steps: 10, Effects: 10, Signals: 10})
	if _, err := interaction.NewDelegate(interaction.DelegateConfig{
		Name: "primitive", Description: "Delegate one primitive value.",
		Deployment: primitive, Budget: budget,
	}); !errors.Is(err, interaction.ErrInvalidDelegate) {
		t.Fatalf("primitive Delegate error = %v", err)
	}

	child := delegateWorkflow(t, "interaction.collision_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse(input), nil
	})
	for name, config := range map[string]interaction.DelegateConfig{
		"blank description": {
			Name: "invalid_blank", Deployment: child, Budget: budget,
		},
		"untrimmed description": {
			Name: "invalid_description", Description: " untrimmed", Deployment: child, Budget: budget,
		},
		"invalid name": {
			Name: "invalid/name", Description: "Use an invalid model capability name.", Deployment: child, Budget: budget,
		},
		"zero budget": {
			Name: "invalid_budget", Description: "Use an invalid zero child budget.", Deployment: child,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := interaction.NewDelegate(config); !errors.Is(err, interaction.ErrInvalidDelegate) {
				t.Fatalf("NewDelegate error = %v", err)
			}
		})
	}
	delegate, err := interaction.NewDelegate(interaction.DelegateConfig{
		Name: "same_name", Description: "Delegate a value to the exact worker.",
		Deployment: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, newDefinitionErr := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.duplicate_delegates", Description: "Reject duplicate managed Delegate names.",
		MaxModelCalls: 2,
		Delegates:     []interaction.Delegate{delegate, delegate},
	}); !errors.Is(newDefinitionErr, interaction.ErrInvalidDefinitionConfig) {
		t.Fatalf("duplicate Delegate error = %v", newDefinitionErr)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.delegate_collision", Description: "Reject an ambiguous model capability manifest.",
		MaxModelCalls: 2, Delegates: []interaction.Delegate{delegate},
	})
	if err != nil {
		t.Fatal(err)
	}
	colliding, err := tool.NewFunc(tool.FuncConfig{
		Name: "same_name", Description: "Return a value locally.",
	}, func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse(input), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := chatclient.New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("unused"), nil
	}), chatclient.Config{})
	if _, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Client: client, Tools: []tool.Tool{colliding},
	}); !errors.Is(err, interaction.ErrInvalidDispatcherConfig) {
		t.Fatalf("name collision error = %v", err)
	}
}

func TestManagedDelegateReturnsArgumentAndStartFailuresToModel(t *testing.T) {
	child := delegateWorkflow(t, "interaction.unavailable_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		return delegateResponse(input), nil
	})
	budget, _ := agent.NewBudget(agent.BudgetConfig{Steps: 10, Effects: 10, Signals: 10})
	delegate, err := interaction.NewDelegate(interaction.DelegateConfig{
		Name: "delegate_unavailable", Description: "Delegate work to an exact worker that may be unavailable.",
		Deployment: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &delegateFailureModel{}
	root := delegateInteraction(t, model, nil, []interaction.Delegate{delegate})
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), root, interactionInput(t, "exercise Delegate failures"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted || model.Calls() != 2 {
		t.Fatalf("result = %#v, model calls = %d", result.Termination(), model.Calls())
	}
	tree, err := engine.CaptureTree(context.Background(), result.ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != 1 {
		t.Fatalf("failed Delegate starts created %d Processes", len(tree.ProcessSnapshots()))
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingManagedDelegateTreeRestoresWithoutRestartingChild(t *testing.T) {
	fixture := newDelegateRestoreFixture(t)
	tree, childID := captureWaitingDelegateTree(t, fixture)
	completeRestoredDelegateTree(t, fixture, tree, childID)
}

type delegateRestoreFixture struct {
	child    agent.Deployment
	root     agent.Deployment
	resolver delegateResolver
	model    *restorableDelegateModel
}

func newDelegateRestoreFixture(t *testing.T) delegateRestoreFixture {
	t.Helper()
	child := pausingDelegateDeployment(t)
	budget, _ := agent.NewBudget(agent.BudgetConfig{Steps: 20, Effects: 20, Signals: 20})
	delegate, err := interaction.NewDelegate(interaction.DelegateConfig{
		Name: "delegate_paused", Description: "Delegate work that may pause before producing its result.",
		Deployment: child, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &restorableDelegateModel{}
	rootDeployment := delegateInteraction(t, model, nil, []interaction.Delegate{delegate})
	resolver := delegateResolver{child.DeploymentRef(): child}
	return delegateRestoreFixture{child: child, root: rootDeployment, resolver: resolver, model: model}
}

func captureWaitingDelegateTree(
	t *testing.T,
	fixture delegateRestoreFixture,
) (agent.TreeSnapshot, agent.ProcessID) {
	t.Helper()
	engine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: fixture.resolver})
	root, err := engine.Start(context.Background(), fixture.root, interactionInput(t, "pause and restore"))
	if err != nil {
		t.Fatal(err)
	}
	tree, childID := awaitWaitingDelegateTree(t, engine, root.ID())
	rootSnapshot := rootProcessSnapshot(tree)
	assertActiveDelegateChild(t, rootSnapshot, childID)
	terminateOriginalDelegateTree(t, engine, root, childID)
	return tree, childID
}

func rootProcessSnapshot(tree agent.TreeSnapshot) agent.ProcessSnapshot {
	for _, snapshot := range tree.ProcessSnapshots() {
		if snapshot.Relation().IsRoot() {
			return snapshot
		}
	}
	return agent.ProcessSnapshot{}
}

func assertActiveDelegateChild(t *testing.T, rootSnapshot agent.ProcessSnapshot, childID agent.ProcessID) {
	t.Helper()
	activeChildren, found, err := interaction.ActiveDelegateChildrenFromSnapshot(rootSnapshot)
	if err != nil || !found || len(activeChildren) != 1 {
		t.Fatalf("active Delegate children = %#v, found = %t, error = %v", activeChildren, found, err)
	}
	if pending, snapshotFound, pendingToolInputFromSnapshotErr := interaction.PendingToolInputFromSnapshot(rootSnapshot); pendingToolInputFromSnapshotErr != nil || snapshotFound {
		t.Fatalf(
			"Delegate-waiting root pending Tool input = %#v, found = %t, error = %v",
			pending,
			snapshotFound,
			pendingToolInputFromSnapshotErr,
		)
	}
	activeChild := activeChildren[0]
	if !activeChild.Valid() || activeChild.ModelCallSequence() != 1 ||
		activeChild.ToolCallIndex() != 0 || activeChild.ToolCall().ID != "call_paused" ||
		activeChild.ChildKey().String() == "" || activeChild.ProcessID() != childID {
		t.Fatalf("active Delegate child = %#v", activeChild)
	}
}

func terminateOriginalDelegateTree(
	t *testing.T,
	engine *agent.Engine,
	root *agent.Process,
	childID agent.ProcessID,
) {
	t.Helper()
	if killErr := root.Kill(context.Background(), "replace captured Delegate tree"); killErr != nil {
		t.Fatal(killErr)
	}
	if result, awaitErr := root.Await(context.Background()); awaitErr != nil || result.Status() != agent.StatusKilled {
		t.Fatalf("original root result = %#v, %v", result.Termination(), awaitErr)
	}
	originalChild, found := engine.Process(childID)
	if !found {
		t.Fatalf("original child %s was not registered", childID)
	}
	if childResult, awaitErr := originalChild.Await(context.Background()); awaitErr != nil || childResult.Status() != agent.StatusCanceled {
		t.Fatalf("original child result = %#v, %v", childResult.Termination(), awaitErr)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func completeRestoredDelegateTree(
	t *testing.T,
	fixture delegateRestoreFixture,
	tree agent.TreeSnapshot,
	childID agent.ProcessID,
) {
	t.Helper()
	restoredEngine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: fixture.resolver})
	restoredRoot, err := restoredEngine.RestoreTree(context.Background(), fixture.root, tree)
	if err != nil {
		t.Fatal(err)
	}
	restoredChild, found := restoredEngine.Process(childID)
	if !found {
		t.Fatalf("restored child %s was not registered", childID)
	}
	if restoredChild.Status() != agent.StatusPaused {
		t.Fatalf("restored child %s status = %s", childID, restoredChild.Status())
	}
	if resumeErr := restoredChild.Resume(context.Background()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	result, err := restoredRoot.Await(context.Background())
	if err != nil || result.Status() != agent.StatusCompleted || fixture.model.Calls() != 2 {
		t.Fatalf("restored result = %#v, error = %v, model calls = %d", result.Termination(), err, fixture.model.Calls())
	}
	finalTree, err := restoredEngine.CaptureTree(context.Background(), restoredRoot.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(finalTree.ProcessSnapshots()) != 2 {
		t.Fatalf("restored tree created duplicate children: %d Processes", len(finalTree.ProcessSnapshots()))
	}
	if err := restoredEngine.Close(); err != nil {
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
	if len(request.Messages) != 3 || len(request.Messages[2].Parts) != 2 {
		return nil, errors.New("Delegate failures were not returned as one Tool result batch")
	}
	first := request.Messages[2].Parts[0].ToolResult
	second := request.Messages[2].Parts[1].ToolResult
	firstText, firstOK := toolResultText(first)
	secondText, secondOK := toolResultText(second)
	if first == nil || !firstOK || !first.IsError || !strings.Contains(firstText, "input contract") ||
		second == nil || !secondOK || !second.IsError || !strings.Contains(secondText, "engine.child.deployment_unavailable") {
		return nil, fmt.Errorf("Delegate failure results = %#v, %#v", first, second)
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

func delegateInteraction(
	t *testing.T,
	model chat.Model,
	tools []tool.Tool,
	delegates []interaction.Delegate,
) agent.Deployment {
	return delegateInteractionWithValidator(t, model, tools, delegates, nil, 3)
}

func delegateInteractionWithValidator(
	t *testing.T,
	model chat.Model,
	tools []tool.Tool,
	delegates []interaction.Delegate,
	validator interaction.CompletionValidator,
	maxModelCalls uint32,
) agent.Deployment {
	t.Helper()
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.delegate_root", Description: "Exercise exact managed worker delegation.",
		MaxModelCalls: maxModelCalls, Delegates: delegates,
		CompletionValidator: validator,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Client: client, Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("delegate-root-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("delegate-root-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func delegateWorkflow[I, O any](t *testing.T, name string, transform workflow.TransformFunc[I, O]) agent.Deployment {
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
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(name + "-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(name + "-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

type delegateResolver map[agent.DeploymentRef]agent.Deployment

func (d delegateResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return agent.Deployment{}, errors.New("delegated Deployment is unavailable")
	}
	return deployment, nil
}

type pausingDelegateDefinition struct {
	descriptor agent.Descriptor
}

type pausingDelegateExecution struct {
	Input delegateRequest `json:"input"`
	Ready bool            `json:"ready"`
}

func pausingDelegateDeployment(t *testing.T) agent.Deployment {
	t.Helper()
	inputSchema, _ := agent.SchemaFor[delegateRequest]()
	outputSchema, _ := agent.SchemaFor[delegateResponse]()
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "interaction.pausing_delegate_worker", Description: "Pause once and then return the delegated value.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &pausingDelegateDefinition{descriptor: descriptor}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: pausingDelegateDispatcher{},
		ImplementationDigest: agent.ComputeDigest([]byte("pausing-delegate-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("pausing-delegate-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (p *pausingDelegateDefinition) Descriptor() agent.Descriptor {
	return p.descriptor
}

func (*pausingDelegateDefinition) Start(input agent.Input) (agent.Execution, error) {
	decoded, err := input.Decode[delegateRequest]()
	if err != nil {
		return nil, err
	}
	return &pausingDelegateExecution{Input: decoded}, nil
}

func (*pausingDelegateDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != "test.pausing_delegate" {
		return nil, errors.New("invalid pausing Delegate state")
	}
	var execution pausingDelegateExecution
	if err := json.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

func (p *pausingDelegateExecution) Step(context.Context, []agent.Signal) (agent.Transition, error) {
	if !p.Ready {
		p.Ready = true
		return agent.Pause(0, "test worker waits at a recoverable boundary")
	}
	output, err := agent.EncodeOutput(delegateResponse{Value: p.Input.Value})
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(0, output)
}

func (p *pausingDelegateExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("test.pausing_delegate", payload)
}

type pausingDelegateDispatcher struct{}

func (pausingDelegateDispatcher) Dispatch(context.Context, agent.EffectRequest, agent.DeltaEmitter) (agent.Settlement, error) {
	return agent.Settlement{}, errors.New("pausing Delegate worker has no dispatcher Effects")
}

func (pausingDelegateDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

func awaitWaitingDelegateTree(
	t *testing.T,
	engine *agent.Engine,
	rootID agent.ProcessID,
) (agent.TreeSnapshot, agent.ProcessID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		tree, err := engine.CaptureTree(ctx, rootID)
		if err != nil {
			t.Fatalf("capture Delegate tree: %v", err)
		}
		var rootWaiting bool
		var childID agent.ProcessID
		for _, snapshot := range tree.ProcessSnapshots() {
			switch snapshot.Relation().Depth() {
			case 0:
				rootWaiting = snapshot.Status() == agent.StatusWaiting
			case 1:
				if snapshot.Status() == agent.StatusPaused {
					childID = snapshot.ProcessID()
				}
			}
		}
		if rootWaiting && childID.Valid() {
			return tree, childID
		}
		runtime.Gosched()
	}
}
