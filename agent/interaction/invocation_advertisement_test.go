package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestInvocationAttributionAndDeferredToolAdvertisement(t *testing.T) {
	model := &attributionModel{}
	var capture attributionCapture
	discover := &callbackTool{
		name: "discover",
		call: func(ctx context.Context, _ string) (string, error) {
			invocation, found := interaction.ToolInvocationFromContext(ctx)
			if !found {
				return "", errors.New("Tool invocation attribution is missing")
			}
			capture.addTool(invocation)
			if err := interaction.AdvertiseTools(ctx, "lookup", "lookup"); err != nil {
				return "", err
			}
			return "lookup is available", nil
		},
	}
	lookup := &callbackTool{
		name: "lookup",
		call: func(ctx context.Context, _ string) (string, error) {
			invocation, found := interaction.ToolInvocationFromContext(ctx)
			if !found {
				return "", errors.New("Tool invocation attribution is missing")
			}
			capture.addTool(invocation)
			return "found", nil
		},
	}
	model.capture = &capture
	deployment := newDeferredDeployment(t, model, []tool.Tool{discover}, []tool.Tool{lookup}, 0)
	process, engine := startDeferredInteraction(t, deployment)
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}

	models, tools := capture.values()
	if len(models) != 3 || len(tools) != 2 {
		t.Fatalf("model/tool invocations = %d/%d, want 3/2", len(models), len(tools))
	}
	wantModelSteps := []uint64{1, 5, 9}
	for index, invocation := range models {
		assertRootInvocation(t, invocation.Relation(), invocation.DeploymentRef(), result.ProcessID(), deployment.DeploymentRef())
		if invocation.ModelCallSequence() != uint32(index+1) || invocation.StepSequence() != wantModelSteps[index] {
			t.Fatalf(
				"model invocation %d sequence/step = %d/%d, want %d/%d",
				index, invocation.ModelCallSequence(), invocation.StepSequence(), index+1, wantModelSteps[index],
			)
		}
	}
	wantToolNames := []string{"discover", "lookup"}
	for index, invocation := range tools {
		parentID, child := invocation.Relation().ParentID()
		if !child || parentID != result.ProcessID() || invocation.Relation().Depth() != 1 || invocation.Relation().RootID() != result.ProcessID() {
			t.Fatalf("Tool relation=%#v", invocation.Relation())
		}
		if _, bound := deployment.resolver[invocation.DeploymentRef()]; !bound {
			t.Fatal("Tool invocation references a different deployment")
		}
		if invocation.ModelCallSequence() != uint32(index+1) || invocation.ToolCallIndex() != 0 ||
			invocation.StepSequence() != 1 || invocation.ToolCall().Name != wantToolNames[index] {
			t.Fatalf("tool invocation %d = %#v", index, invocation)
		}
	}
	for index := 1; index < len(models); index++ {
		if models[index-1].EffectID() == models[index].EffectID() {
			t.Fatalf("model Effects %d and %d share identity %s", index-1, index, models[index].EffectID().String())
		}
	}
	if tools[0].EffectID() == tools[1].EffectID() {
		t.Fatalf("Tool Effects share identity %s", tools[0].EffectID().String())
	}
	assertModelToolResultPolicy(t, tools[0])
}

func assertModelToolResultPolicy(t *testing.T, invocation interaction.ToolInvocation) {
	t.Helper()
	call := invocation.ToolCall()
	structured, err := chat.NewJSONToolOutput(json.RawMessage(`{"value":42}`))
	if err != nil {
		t.Fatal(err)
	}
	success, present := invocation.ModelResult(structured, nil)
	wantSuccess := chat.ToolResult{ID: call.ID, Name: call.Name, Output: structured}
	if !present || !reflect.DeepEqual(success, wantSuccess) {
		t.Fatalf("success = %#v, present = %t", success, present)
	}
	empty, present := invocation.ModelResult(chat.ToolOutput{}, nil)
	if !present || !reflect.DeepEqual(empty, chat.ToolResult{ID: call.ID, Name: call.Name}) {
		t.Fatalf("empty success = %#v, present = %t", empty, present)
	}
	invalidOutput := chat.ToolOutput{Details: json.RawMessage(`{`)}
	invalid, present := invocation.ModelResult(invalidOutput, nil)
	wantInvalid := chat.ToolResult{
		ID: call.ID, Name: call.Name, IsError: true,
		Output: chat.NewTextToolOutput("error: tool \"" + call.Name +
			"\" failed: tool returned invalid output: chat: invalid tool output: details must be one valid RFC 7493 JSON document"),
	}
	if !present || !reflect.DeepEqual(invalid, wantInvalid) {
		t.Fatalf("invalid output result = %#v, present = %t", invalid, present)
	}

	diagnostic := strings.Repeat("x", 3_000)
	failure, present := invocation.ModelResult(chat.NewTextToolOutput("ignored"), errors.New(diagnostic))
	wantFailure := chat.ToolResult{
		ID: call.ID, Name: call.Name,
		Output:  chat.NewTextToolOutput("error: tool \"" + call.Name + "\" failed: " + diagnostic[:2_048]),
		IsError: true,
	}
	if !present || !reflect.DeepEqual(failure, wantFailure) {
		t.Fatalf("failure = %#v, present = %t", failure, present)
	}

	controlCauses := []error{
		interaction.HostFailure(errors.New("projection unavailable")),
		context.Canceled,
		context.DeadlineExceeded,
		interaction.RequireToolInput(
			json.RawMessage(`"continue?"`),
			json.RawMessage(`{"type":"boolean"}`),
			json.RawMessage(`{"stage":"waiting"}`),
		),
	}
	for _, cause := range controlCauses {
		if result, present := invocation.ModelResult(invalidOutput, cause); present || !reflect.DeepEqual(result, chat.ToolResult{}) {
			t.Fatalf("control cause %v produced %#v, present = %t", cause, result, present)
		}
	}
	if result, present := (interaction.ToolInvocation{}).ModelResult(chat.NewTextToolOutput("ignored"), nil); present || !reflect.DeepEqual(result, chat.ToolResult{}) {
		t.Fatalf("invalid invocation produced %#v, present = %t", result, present)
	}
}

func TestAdvertiseToolsRejectsUnavailableAndInvalidNames(t *testing.T) {
	if err := interaction.AdvertiseTools(context.Background(), "hidden"); !errors.Is(err, interaction.ErrToolAdvertisementUnavailable) {
		t.Fatalf("outside invocation error = %v", err)
	}

	errorsSeen := make(chan []error, 1)
	validate := &callbackTool{
		name: "validate",
		call: func(ctx context.Context, _ string) (string, error) {
			errorsSeen <- []error{
				interaction.AdvertiseTools(ctx),
				interaction.AdvertiseTools(ctx, ""),
				interaction.AdvertiseTools(ctx, " hidden"),
				interaction.AdvertiseTools(ctx, "validate"),
				interaction.AdvertiseTools(ctx, "unknown"),
			}
			if err := interaction.AdvertiseTools(ctx, "hidden"); err != nil {
				return "", err
			}
			return "validated", nil
		},
	}
	hidden := &callbackTool{name: "hidden", call: func(context.Context, string) (string, error) {
		return "hidden", nil
	}}
	model := &manifestScriptModel{scripts: []manifestScript{
		{wantTools: []string{"validate"}, response: toolCallResponse(chat.ToolCall{ID: "call_validate", Name: "validate", Arguments: `{}`})},
		{wantTools: []string{"validate", "hidden"}, response: textResponse("done")},
	}}
	deployment := newDeferredDeployment(t, model, []tool.Tool{validate}, []tool.Tool{hidden}, 0)
	process, engine := startDeferredInteraction(t, deployment)
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	for index, err := range <-errorsSeen {
		if !errors.Is(err, interaction.ErrInvalidToolAdvertisement) {
			t.Fatalf("invalid advertisement %d error = %v", index, err)
		}
	}
}

func TestUnsuccessfulToolCallDiscardsStagedAdvertisements(t *testing.T) {
	for _, invalidOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid_output=%t", invalidOutput), func(t *testing.T) {
			failing := &callbackTool{
				name: "failing",
				call: func(ctx context.Context, _ string) (string, error) {
					if err := interaction.AdvertiseTools(ctx, "hidden"); err != nil {
						return "", err
					}
					if invalidOutput {
						return "", nil
					}
					return "", errors.New("external failure")
				},
			}
			hidden := &callbackTool{name: "hidden", call: func(context.Context, string) (string, error) {
				return "hidden", nil
			}}
			model := &manifestScriptModel{scripts: []manifestScript{
				{wantTools: []string{"failing"}, response: toolCallResponse(chat.ToolCall{ID: "call_failing", Name: "failing", Arguments: `{}`})},
				{wantTools: []string{"failing"}, response: textResponse("recovered")},
			}}
			var executable tool.Tool = failing
			if invalidOutput {
				executable = &invalidOutputTool{Tool: failing}
			}
			deployment := newDeferredDeployment(t, model, []tool.Tool{executable}, []tool.Tool{hidden}, 0)
			process, engine := startDeferredInteraction(t, deployment)
			result, err := process.Await(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
			if result.Status() != agent.StatusCompleted {
				t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
			}
		})
	}
}

type invalidOutputTool struct {
	tool.Tool
}

func (i *invalidOutputTool) Call(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	output, err := i.Tool.Call(ctx, invocation)
	output.Details = json.RawMessage(`{`)
	return output, err
}

func TestToolInputCheckpointKeepsOnlyCompletedToolAdvertisements(t *testing.T) {
	prefix := &callbackTool{
		name: "prefix",
		call: func(ctx context.Context, _ string) (string, error) {
			if err := interaction.AdvertiseTools(ctx, "prefix_hidden"); err != nil {
				return "", err
			}
			return "prefix complete", nil
		},
	}
	waiting := &callbackTool{
		name: "waiting",
		call: func(ctx context.Context, _ string) (string, error) {
			if _, resumed := interaction.ToolInputContinuationFromContext(ctx); resumed {
				return "continued", nil
			}
			if err := interaction.AdvertiseTools(ctx, "waiting_hidden"); err != nil {
				return "", err
			}
			return "", interaction.RequireToolInput(
				json.RawMessage(`"continue?"`),
				json.RawMessage(`{"type":"boolean"}`),
				json.RawMessage(`{"stage":"waiting"}`),
			)
		},
	}
	deferred := []tool.Tool{
		&callbackTool{name: "prefix_hidden", call: successfulCallback},
		&callbackTool{name: "waiting_hidden", call: successfulCallback},
	}
	model := &manifestScriptModel{scripts: []manifestScript{
		{
			wantTools: []string{"prefix", "waiting"},
			response: toolCallsResponse(
				chat.ToolCall{ID: "call_prefix", Name: "prefix", Arguments: `{}`},
				chat.ToolCall{ID: "call_waiting", Name: "waiting", Arguments: `{}`},
			),
		},
		{
			wantTools: []string{"prefix", "waiting", "prefix_hidden"},
			response:  textResponse("done"),
		},
	}}
	deployment := newDeferredDeployment(t, model, []tool.Tool{prefix, waiting}, deferred, 0)
	process, engine := startDeferredInteraction(t, deployment)
	_, pending := captureToolInput(t, engine, process)
	toolProcess := pendingToolProcess(t, engine, pending)
	signalID, err := agent.ParseSignalID("signal:advertisement-checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	signal, err := pending.ResponseSignal(signalID, json.RawMessage(`true`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliverSignalErr := toolProcess.DeliverSignals(context.Background(), signal); deliverSignalErr != nil || !accepted {
		t.Fatalf("DeliverSignals accepted = %t, error = %v", accepted, deliverSignalErr)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
}

func TestParallelAdvertisementsCommitInModelToolCallOrder(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	secondFinished := make(chan struct{})
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	invocations := make(chan interaction.ToolInvocation, 2)
	first := &scheduledTool{
		name: "first",
		call: func(ctx context.Context, _ string) (string, error) {
			invocation, found := interaction.ToolInvocationFromContext(ctx)
			if !found {
				return "", errors.New("first Tool invocation attribution is missing")
			}
			invocations <- invocation
			if err := interaction.AdvertiseTools(ctx, "first_hidden"); err != nil {
				return "", err
			}
			close(firstStarted)
			<-firstRelease
			return "first result", nil
		},
	}
	second := &scheduledTool{
		name: "second",
		call: func(ctx context.Context, _ string) (string, error) {
			invocation, found := interaction.ToolInvocationFromContext(ctx)
			if !found {
				return "", errors.New("second Tool invocation attribution is missing")
			}
			invocations <- invocation
			if err := interaction.AdvertiseTools(ctx, "second_hidden"); err != nil {
				return "", err
			}
			close(secondStarted)
			<-secondRelease
			close(secondFinished)
			return "second result", nil
		},
	}
	deferred := []tool.Tool{
		&callbackTool{name: "first_hidden", call: successfulCallback},
		&callbackTool{name: "second_hidden", call: successfulCallback},
	}
	model := &manifestScriptModel{scripts: []manifestScript{
		{
			wantTools: []string{"first", "second"},
			response: toolCallsResponse(
				chat.ToolCall{ID: "call_first", Name: "first", Arguments: `{}`},
				chat.ToolCall{ID: "call_second", Name: "second", Arguments: `{}`},
			),
		},
		{wantTools: []string{"first", "second", "first_hidden", "second_hidden"}, response: textResponse("done")},
	}}
	deployment := newDeferredDeployment(t, model, []tool.Tool{first, second}, deferred, 2)
	process, engine := startDeferredInteraction(t, deployment)
	<-firstStarted
	<-secondStarted
	close(secondRelease)
	<-secondFinished
	close(firstRelease)
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	firstInvocation := <-invocations
	secondInvocation := <-invocations
	byName := map[string]interaction.ToolInvocation{
		firstInvocation.ToolCall().Name:  firstInvocation,
		secondInvocation.ToolCall().Name: secondInvocation,
	}
	if byName["first"].ToolCallIndex() != 0 || byName["second"].ToolCallIndex() != 1 {
		t.Fatalf(
			"first/second ToolCall indices = %d/%d, want 0/1",
			byName["first"].ToolCallIndex(), byName["second"].ToolCallIndex(),
		)
	}
	if byName["first"].EffectID() == byName["second"].EffectID() {
		t.Fatalf(
			"parallel Tool EffectIDs = %s/%s, want independent call identities",
			byName["first"].EffectID().String(), byName["second"].EffectID().String(),
		)
	}
}

func TestDelegateChildKeyIsDeterministicAndSequenceScoped(t *testing.T) {
	call := chat.ToolCall{ID: "call_delegate", Name: "delegate", Arguments: `{"task":"inspect"}`}
	first, err := interaction.DelegateChildKey(1, call)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := interaction.DelegateChildKey(1, call)
	if err != nil {
		t.Fatal(err)
	}
	next, err := interaction.DelegateChildKey(2, call)
	if err != nil {
		t.Fatal(err)
	}
	if first != repeated || first == next {
		t.Fatalf("keys first/repeated/next = %s/%s/%s", first.String(), repeated.String(), next.String())
	}
	if _, err := interaction.DelegateChildKey(0, call); !errors.Is(err, interaction.ErrInvalidDelegate) {
		t.Fatalf("zero sequence error = %v", err)
	}
	if _, err := interaction.DelegateChildKey(1, chat.ToolCall{}); !errors.Is(err, interaction.ErrInvalidDelegate) {
		t.Fatalf("invalid ToolCall error = %v", err)
	}
}

func TestToolSetRejectsDuplicateAndTypedNilDeferredTools(t *testing.T) {
	duplicate := &callbackTool{name: "duplicate", call: successfulCallback}
	var typedNil *callbackTool
	tests := []struct {
		name   string
		config interaction.ToolSetConfig
	}{
		{name: "initial and deferred", config: interaction.ToolSetConfig{Tools: []tool.Tool{duplicate}, DeferredTools: []tool.Tool{duplicate}}},
		{name: "two deferred", config: interaction.ToolSetConfig{DeferredTools: []tool.Tool{duplicate, duplicate}}},
		{name: "typed nil", config: interaction.ToolSetConfig{DeferredTools: []tool.Tool{typedNil}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.config.Name = "interaction.invalid-tools"
			test.config.Description = "Validate duplicate and typed-nil Tool bindings."
			test.config.ImplementationDigest = agent.ComputeDigest([]byte("tool-validation-implementation"))
			test.config.ConfigurationDigest = agent.ComputeDigest([]byte("tool-validation-configuration"))
			if _, err := interaction.NewToolSet(test.config); !errors.Is(err, interaction.ErrInvalidToolSet) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type attributionCapture struct {
	mu     sync.Mutex
	models []interaction.ModelInvocation
	tools  []interaction.ToolInvocation
}

func (a *attributionCapture) addModel(invocation interaction.ModelInvocation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.models = append(a.models, invocation)
}

func (a *attributionCapture) addTool(invocation interaction.ToolInvocation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools = append(a.tools, invocation)
}

func (a *attributionCapture) values() ([]interaction.ModelInvocation, []interaction.ToolInvocation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.models), slices.Clone(a.tools)
}

type attributionModel struct {
	mu      sync.Mutex
	calls   int
	capture *attributionCapture
}

func (a *attributionModel) Call(ctx context.Context, request *chat.Request) (*chat.Response, error) {
	invocation, found := interaction.ModelInvocationFromContext(ctx)
	if !found {
		return nil, errors.New("model invocation attribution is missing")
	}
	a.capture.addModel(invocation)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	switch a.calls {
	case 1:
		if !slices.Equal(toolNames(request.Tools), []string{"discover"}) {
			return nil, fmt.Errorf("first manifest = %v", toolNames(request.Tools))
		}
		return toolCallResponse(chat.ToolCall{ID: "call_discover", Name: "discover", Arguments: `{}`}), nil
	case 2:
		if !slices.Equal(toolNames(request.Tools), []string{"discover", "lookup"}) {
			return nil, fmt.Errorf("second manifest = %v", toolNames(request.Tools))
		}
		return toolCallResponse(chat.ToolCall{ID: "call_lookup", Name: "lookup", Arguments: `{}`}), nil
	case 3:
		if !slices.Equal(toolNames(request.Tools), []string{"discover", "lookup"}) {
			return nil, fmt.Errorf("third manifest = %v", toolNames(request.Tools))
		}
		return textResponse("done"), nil
	default:
		return nil, errors.New("unexpected model call")
	}
}

type manifestScript struct {
	wantTools []string
	response  *chat.Response
}

type manifestScriptModel struct {
	mu      sync.Mutex
	calls   int
	scripts []manifestScript
}

func (m *manifestScriptModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls >= len(m.scripts) {
		return nil, errors.New("unexpected model call")
	}
	script := m.scripts[m.calls]
	m.calls++
	if got := toolNames(request.Tools); !slices.Equal(got, script.wantTools) {
		return nil, fmt.Errorf("model call %d manifest = %v, want %v", m.calls, got, script.wantTools)
	}
	return script.response.Clone(), nil
}

type callbackTool struct {
	name string
	call func(context.Context, string) (string, error)
}

func (c *callbackTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name: c.name, Description: "Exercise Interaction invocation and advertisement contracts.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
}

func (c *callbackTool) Call(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	result, err := c.call(ctx, string(invocation.Arguments()))
	return chat.NewTextToolOutput(result), err
}

func successfulCallback(context.Context, string) (string, error) { return "done", nil }

func newDeferredDeployment(t *testing.T, model chat.Model, initial []tool.Tool, deferred []tool.Tool, maxConcurrent int) interactionDeployment {
	t.Helper()
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return configuredInteraction(t, interaction.DefinitionConfig{
		Name: "interaction.deferred", Description: "Verify recoverable deferred Tool advertisement.", MaxModelCalls: 4, MaxConcurrentToolCalls: maxConcurrent,
	}, interaction.DispatcherConfig{Client: client}, interaction.ToolSetConfig{Tools: initial, DeferredTools: deferred})
}

func startDeferredInteraction(t *testing.T, deployment interactionDeployment) (*agent.Process, *agent.Engine) {
	t.Helper()
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment.Deployment, interactionInput(t, "test deferred Tools"))
	if err != nil {
		_ = engine.Close(context.WithoutCancel(t.Context()))
		t.Fatal(err)
	}
	return process, engine
}

func assertRootInvocation(
	t *testing.T,
	relation agent.ProcessRelation,
	deploymentRef agent.DeploymentRef,
	processID agent.ProcessID,
	wantDeploymentRef agent.DeploymentRef,
) {
	t.Helper()
	if !relation.IsRoot() || relation.ProcessID() != processID || relation.RootID() != processID {
		t.Fatalf("relation = %#v, want root %s", relation, processID.String())
	}
	if deploymentRef != wantDeploymentRef {
		t.Fatalf("DeploymentRef = %s, want %s", deploymentRef.String(), wantDeploymentRef.String())
	}
}

func toolNames(definitions []chat.ToolDefinition) []string {
	names := make([]string, len(definitions))
	for index := range definitions {
		names[index] = definitions[index].Name
	}
	return names
}
