package interaction

import (
	"context"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type boundTool struct {
	executable tool.Binding
	deferred   bool
	direct     bool
	concurrent func(tool.Invocation) (string, bool)
}

type toolDispatcher struct {
	tools               map[string]boundTool
	initialDefinitions  []chat.ToolDefinition
	deferredToolNames   map[string]struct{}
	observer            ToolObserver
	observationFailures observationFailureCounters
}

func (*toolDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy { return agent.ReplayPolicyNever }

func (t *toolDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, _ agent.DeltaEmitter) (agent.Settlement, error) {
	if ctx == nil {
		panic(errors.New("interaction: nil Context"))
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return agent.Settlement{}, err
	}
	if envelope.Operation != operationToolCall || envelope.ToolCall == nil {
		return agent.Settlement{}, errors.New("interaction: Tool dispatcher requires one tool_call")
	}
	call := envelope.ToolCall.Invocation
	resume := envelope.ToolCall.Resume
	if resume != nil {
		ctx = withToolInputContinuation(ctx, ToolInputContinuation{
			state: resume.Checkpoint.InputRequest.continuationState, response: resume.InputResponse,
		})
	}
	prepared := t.prepareToolCall(call.Call)
	result, advertised, required, err := t.callTool(ctx, request, call.ModelCallSequence, call.ToolCallIndex, prepared)
	if err != nil {
		return agent.Settlement{}, err
	}
	outcome := toolDispatchResult{Completion: &toolCallResult{
		Result: result, Direct: prepared.binding != nil && prepared.binding.direct && !result.IsError, AdvertisedToolNames: advertised,
	}}
	if required != nil {
		count := uint32(0)
		if resume != nil {
			count = resume.Checkpoint.PauseCount
		}
		outcome = toolDispatchResult{Checkpoint: &toolCheckpoint{PauseCount: count + 1, InputRequest: *required}}
	}
	payload, err := encodeProtocol(signalEnvelope{Operation: operationToolCall, ToolResult: &outcome})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload)
}

func (t *toolDispatcher) bindTool(executable tool.Tool, deferred bool) error {
	binding, err := tool.Bind(executable)
	if err != nil {
		return err
	}
	definition := binding.Contract().Definition()
	if _, duplicate := t.tools[definition.Name]; duplicate {
		return fmt.Errorf("duplicate tool name %q", definition.Name)
	}
	direct, err := directResultCapability(executable)
	if err != nil {
		return err
	}
	concurrent, err := concurrentToolCapability(executable)
	if err != nil {
		return err
	}
	t.tools[definition.Name] = boundTool{
		executable: binding, deferred: deferred,
		direct: direct, concurrent: concurrent,
	}
	if deferred {
		t.deferredToolNames[definition.Name] = struct{}{}
	} else {
		t.initialDefinitions = append(t.initialDefinitions, definition)
	}
	return nil
}

func (t *toolDispatcher) callTool(
	ctx context.Context,
	request agent.EffectRequest,
	modelCallSequence uint32,
	toolCallIndex uint32,
	prepared preparedToolCall,
) (
	result chat.ToolResult,
	advertisedToolNames []string,
	required *ToolInputRequest,
	err error,
) {
	call := prepared.call
	if prepared.rejection != nil {
		return prepared.rejection.Clone(), nil, nil, nil
	}
	invocation := toolInvocationFromRequest(
		request, modelCallSequence, toolCallIndex, call,
	)
	t.observeToolStarted(ctx, invocation)
	defer func() {
		settlement := ToolSettlement{}
		switch {
		case required != nil:
			settlement.InputRequired = true
		case result.ID != "":
			settlement.Result = &result
		case err != nil:
			settlement.Failure = boundedDiagnostic(err.Error())
			settlement.Unknown = true
		}
		t.observeToolSettled(ctx, invocation, settlement)
	}()
	binding := prepared.binding
	advertiser := newToolAdvertiser(t.deferredToolNames)
	ctx = withToolInvocation(ctx, invocation)
	ctx = withToolAdvertiser(ctx, advertiser)
	defer func() {
		if recovered := recover(); recovered != nil {
			result = chat.ToolResult{}
			advertisedToolNames = nil
			required = nil
			err = fmt.Errorf("tool panicked: %v", recovered)
		}
	}()
	output, err := binding.executable.Call(ctx, prepared.invocation)
	if isHostOrContextError(err) {
		return chat.ToolResult{}, nil, nil, err
	}
	if err != nil {
		if inputRequired, ok := errors.AsType[*ToolInputRequiredError](err); ok {
			request, valid := inputRequired.inputRequest()
			if !valid {
				return chat.ToolResult{}, nil, nil, ErrInvalidToolInputRequest
			}
			return chat.ToolResult{}, nil, &request, nil
		}
	}
	result, err = ModelToolResult(call, output, err)
	if err != nil {
		return chat.ToolResult{}, nil, nil, err
	}
	if result.IsError {
		return result, nil, nil, nil
	}
	return result, advertiser.advertisedNames(), nil, nil
}

func (t *toolDispatcher) prepareToolCall(call chat.ToolCall) preparedToolCall {
	prepared := preparedToolCall{call: call}
	binding, found := t.tools[call.Name]
	if !found {
		result := rejectedToolResult(call, fmt.Sprintf("tool %q is not available", call.Name))
		prepared.rejection = &result
		return prepared
	}
	prepared.binding = &binding
	invocation, err := binding.executable.Contract().Prepare(call)
	if err != nil {
		result := rejectedToolResult(call, "invalid arguments: "+boundedDiagnostic(err.Error()))
		prepared.rejection = &result
		return prepared
	}
	prepared.invocation = invocation
	return prepared
}

func (t *toolDispatcher) observeToolStarted(ctx context.Context, invocation ToolInvocation) {
	if t.observer == nil {
		return
	}
	defer recordObserverPanic(&t.observationFailures.toolStartedPanics)
	t.observer.OnToolStarted(ctx, invocation)
}

func (t *toolDispatcher) observeToolSettled(ctx context.Context, invocation ToolInvocation, settlement ToolSettlement) {
	if t.observer == nil {
		return
	}
	if settlement.Result != nil {
		settlement.Result = new(settlement.Result.Clone())
	}
	defer recordObserverPanic(&t.observationFailures.toolSettledPanics)
	t.observer.OnToolSettled(ctx, invocation, settlement)
}

func directResultCapability(executable tool.Tool) (direct bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			direct = false
			err = fmt.Errorf("direct-result capability panicked: %v", recovered)
		}
	}()
	capability, found, err := tool.Capability[DirectResultTool](executable)
	if err != nil {
		return false, fmt.Errorf("direct-result capability: %w", err)
	}
	if !found {
		return false, nil
	}
	return capability.ReturnsDirectResult(), nil
}

func concurrentToolCapability(executable tool.Tool) (declared func(tool.Invocation) (string, bool), err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			declared = nil
			err = fmt.Errorf("concurrency policy declaration panicked: %v", recovered)
		}
	}()
	capability, found, err := tool.Capability[ConcurrentTool](executable)
	if err != nil {
		return nil, fmt.Errorf("concurrency capability: %w", err)
	}
	if !found {
		return nil, nil
	}
	return capability.ConcurrencyPolicy(), nil
}
