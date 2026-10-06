package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type preparedToolCall struct {
	call       chat.ToolCall
	binding    tool.Binding
	invocation tool.Invocation
	rejection  *chat.ToolResult
}

func (p preparedToolCall) completion(result chat.ToolResult, rejected bool, advertised []string) *toolCallResult {
	completion := newToolCallResult(result)
	completion.AdvertisedToolNames = advertised
	if rejected {
		completion.Disposition = ResultRejected
	}
	return &completion
}

// toolDispatcher executes bound Tools. Its manifest is the one index of each
// Tool's scheduling policy; the ToolSet shares it with the Interaction.
type toolDispatcher struct {
	bindings            map[string]tool.Binding
	manifest            toolManifest
	observer            ToolObserver
	observationFailures observationFailureCounters
}

func newToolDispatcher(observer ToolObserver) *toolDispatcher {
	return &toolDispatcher{
		bindings: make(map[string]tool.Binding),
		manifest: toolManifest{entries: make(map[string]toolManifestEntry)},
		observer: observer,
	}
}

func (*toolDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy { return agent.ReplayPolicyNever }

func (t *toolDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, _ agent.DeltaEmitter) (agent.Settlement, error) {
	ctx = agent.RequireContext(ctx)
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return protocolFailureSettlement(err)
	}
	if envelope.operation() != operationToolCall || envelope.ToolCall == nil {
		return protocolFailureSettlement(errors.New("interaction: Tool dispatcher requires one tool_call"))
	}
	call := envelope.ToolCall.Invocation
	resume := envelope.ToolCall.Resume
	if resume != nil {
		ctx = withToolInputContinuation(ctx, ToolInputContinuation{
			state: resume.InputRequest.continuationState, response: resume.InputResponse,
		})
	}
	prepared := t.prepareToolCall(call.Call)
	result, advertised, required, rejected, err := t.callTool(ctx, request, call.ModelCallSequence, call.ToolCallIndex, prepared)
	if err != nil {
		return agent.Settlement{}, err
	}
	outcome := toolDispatchResult{Completion: prepared.completion(result, rejected, advertised)}
	if required != nil {
		outcome = toolDispatchResult{InputRequest: required}
	}
	return outcome.settlement()
}

func protocolFailureSettlement(cause error) (agent.Settlement, error) {
	payload, err := jsonv2.Marshal(agent.NormalizeDiagnostic(cause.Error()))
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(agent.SettlementStatusFailed, payload)
}

func (t *toolDispatcher) bindTool(executable tool.Tool, deferred bool) error {
	binding, err := tool.Bind(executable)
	if err != nil {
		return err
	}
	definition := binding.Contract().Definition()
	if _, duplicate := t.bindings[definition.Name]; duplicate {
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
	t.bindings[definition.Name] = binding
	t.manifest.entries[definition.Name] = toolManifestEntry{
		contract: binding.Contract(), deferred: deferred, direct: direct, concurrent: concurrent,
	}
	if !deferred {
		t.manifest.initialDefinitions = append(t.manifest.initialDefinitions, definition)
	}
	return nil
}

func (t *toolDispatcher) callTool(
	ctx context.Context,
	request agent.EffectRequest,
	modelCallSequence uint64,
	toolCallIndex uint32,
	prepared preparedToolCall,
) (
	result chat.ToolResult,
	advertisedToolNames []string,
	required *toolInputRequest,
	rejected bool,
	err error,
) {
	call := prepared.call
	if prepared.rejection != nil {
		return prepared.rejection.Clone(), nil, nil, true, nil
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
			settlement.Failure = agent.NormalizeDiagnostic(err.Error())
			settlement.Unknown = true
			if evidence, ok := errors.AsType[*tool.CallError](err); ok && evidence.Validate() == nil {
				settlement.Evidence = new(evidence.Evidence())
			}
		}
		t.observeToolSettled(ctx, invocation, settlement)
	}()
	binding := prepared.binding
	advertiser := newToolAdvertiser(t.manifest)
	ctx = withToolInvocation(ctx, invocation)
	ctx = withToolAdvertiser(ctx, advertiser)
	defer func() {
		advertiser.close()
		if recovered := recover(); recovered != nil {
			result = chat.ToolResult{}
			advertisedToolNames = nil
			required = nil
			err = &agent.CallbackPanicError{Operation: "Tool.Call", Value: recovered}
		}
	}()
	output, err := binding.Call(ctx, prepared.invocation)
	names := advertiser.close()
	result, required, rejected, err = modelToolResult(call, output, err)
	if err != nil {
		return chat.ToolResult{}, nil, nil, false, err
	}
	if required != nil {
		return chat.ToolResult{}, nil, required, false, nil
	}
	if result.IsError {
		return result, nil, nil, rejected, nil
	}
	return result, names, nil, false, nil
}

func (t *toolDispatcher) prepareToolCall(call chat.ToolCall) preparedToolCall {
	prepared := preparedToolCall{call: call}
	binding, found := t.bindings[call.Name]
	if !found {
		result := rejectedToolResult(call, fmt.Sprintf("tool %q is not available", call.Name))
		prepared.rejection = &result
		return prepared
	}
	prepared.binding = binding
	invocation, err := binding.Contract().Prepare(call)
	if err != nil {
		result := rejectedToolResult(call, "invalid arguments: "+agent.NormalizeDiagnostic(err.Error()))
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
	attempt, _ := invocation.AttemptID()
	defer t.observationFailures.recordPanic(toolStartedCallback, t.observer, invocation.Relation().ProcessID(), invocation.EffectID(), attempt)
	t.observer.OnToolStarted(ctx, invocation)
}

func (t *toolDispatcher) observeToolSettled(ctx context.Context, invocation ToolInvocation, settlement ToolSettlement) {
	if t.observer == nil {
		return
	}
	if settlement.Result != nil {
		settlement.Result = new(settlement.Result.Clone())
	}
	if settlement.Evidence != nil {
		settlement.Evidence = new(settlement.Evidence.Clone())
	}
	attempt, _ := invocation.AttemptID()
	defer t.observationFailures.recordPanic(toolSettledCallback, t.observer, invocation.Relation().ProcessID(), invocation.EffectID(), attempt)
	t.observer.OnToolSettled(ctx, invocation, settlement)
}

func directResultCapability(executable tool.Tool) (direct bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			direct = false
			err = &agent.CallbackPanicError{Operation: "DirectResultTool.ReturnsDirectResult", Value: recovered}
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
			err = &agent.CallbackPanicError{Operation: "ConcurrentTool.ConcurrencyPolicy", Value: recovered}
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
