package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// errToolInputValidatorDefect is the fixed, model-safe diagnostic for a tool
// whose input validator panicked. The recovered value is an internal fault, so
// its classification replaces it and it never reaches the model.
var errToolInputValidatorDefect = errors.New("interaction: tool input validation failed")

type preparedToolCall struct {
	call       chat.ToolCall
	binding    tool.Binding
	invocation tool.Invocation
	rejection  *chat.ToolResult
	// defect holds an input-validator panic, kept separate from rejection: it is
	// an internal fault that settles the dispatch as a definite failure rather
	// than a model-correctable argument error.
	defect error
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
// Tool's scheduling policy; the ToolSet shares it with the Interaction. The
// executable Tools and their advertising capability stay here, never in the
// manifest, which the Interaction retains without executables.
type toolDispatcher struct {
	bindings            map[string]tool.Binding
	advertisers         map[string]AdvertisingTool
	manifest            toolManifest
	observer            ToolObserver
	observationFailures observationFailureCounters
}

func newToolDispatcher(observer ToolObserver) *toolDispatcher {
	return &toolDispatcher{
		bindings:    make(map[string]tool.Binding),
		advertisers: make(map[string]AdvertisingTool),
		manifest:    toolManifest{entries: make(map[string]toolManifestEntry)},
		observer:    observer,
	}
}

func (*toolDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func (t *toolDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	ctx = run.RequireContext(ctx)
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return failedSettlement(err)
	}
	if envelope.operation() != operationToolCall || envelope.ToolCall == nil {
		return failedSettlement(errors.New("interaction: Tool dispatcher requires one tool_call"))
	}
	call := envelope.ToolCall.Invocation
	resume := envelope.ToolCall.Resume
	var continuation *tool.Continuation
	if resume != nil {
		input, resumeErr := resume.InputRequest.Continuation(resume.InputResponse)
		if resumeErr != nil {
			return failedSettlement(resumeErr)
		}
		continuation = &input
	}
	prepared := t.prepareToolCall(call.Call, continuation)
	if prepared.defect != nil {
		// A validator defect is settled before Tool.Call runs, as a definite
		// failure that propagates to the parent, never a model-visible result.
		return failedSettlement(errToolInputValidatorDefect)
	}
	result, advertised, required, rejected, err := t.callTool(ctx, request, prepared)
	if err != nil && result.ID == "" {
		return run.Settlement{}, err
	}
	outcome := toolDispatchResult{Completion: prepared.completion(result, rejected, advertised)}
	if err != nil {
		outcome.AdvertisementFailure = run.NormalizeDiagnostic(err.Error())
	}
	if required != nil {
		outcome = toolDispatchResult{InputRequest: required}
	}
	return outcome.settlement()
}

func (t *toolDispatcher) bindTool(executable tool.Tool, deferred bool) error {
	binding, err := tool.Bind(executable)
	if err != nil {
		return err
	}
	entry := toolManifestEntry{contract: binding.Contract(), deferred: deferred}
	publicName := entry.modelDefinition().Name
	if publicName == "" {
		return fmt.Errorf("tool %q has no representable model name", entry.contract.Name())
	}
	if _, duplicate := t.bindings[publicName]; duplicate {
		return fmt.Errorf("duplicate tool name %q", publicName)
	}
	direct, err := directResultCapability(executable)
	if err != nil {
		return err
	}
	concurrent, err := concurrentToolCapability(executable)
	if err != nil {
		return err
	}
	advertiser, err := advertisingCapability(executable)
	if err != nil {
		return err
	}
	t.bindings[publicName] = binding
	if advertiser != nil {
		t.advertisers[publicName] = advertiser
	}
	entry.direct, entry.concurrent = direct, concurrent
	t.manifest.entries[publicName] = entry
	if !deferred {
		t.manifest.initialNames = append(t.manifest.initialNames, publicName)
	}
	return nil
}

func (t *toolDispatcher) callTool(
	ctx context.Context,
	request run.EffectRequest,
	prepared preparedToolCall,
) (
	result chat.ToolResult,
	advertisedToolNames []string,
	required *tool.InputRequest,
	rejected bool,
	err error,
) {
	call := prepared.call
	if prepared.rejection != nil {
		return prepared.rejection.Clone(), nil, nil, true, nil
	}
	invocation := toolInvocationFromRequest(request, call)
	t.observeToolStarted(ctx, invocation)
	defer func() {
		settlement := ToolSettlement{}
		switch {
		case required != nil:
			settlement.InputRequired = true
		case result.ID != "":
			settlement.Result = &result
		case err != nil:
			settlement.Failure = run.NormalizeDiagnostic(err.Error())
			if evidence, ok := errors.AsType[*tool.CallError](err); ok && evidence.Validate() == nil {
				settlement.Evidence = new(evidence.Evidence())
			}
		}
		t.observeToolSettled(ctx, invocation, settlement)
	}()
	binding := prepared.binding
	ctx = withToolInvocation(ctx, invocation)
	defer func() {
		if recovered := recover(); recovered != nil {
			result = chat.ToolResult{}
			advertisedToolNames = nil
			required = nil
			err = &run.CallbackPanicError{Operation: "Tool.Call", Value: recovered}
		}
	}()
	output, err := binding.Call(ctx, prepared.invocation)
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
	names, err := t.adviseDeferred(ctx, call.Name, prepared.invocation, output)
	if err != nil {
		return result, nil, nil, false, err
	}
	return result, names, nil, false, nil
}

// adviseDeferred collects the deferred Tool names a successful call reveals.
// Because it runs only after a successful result, a rejected, failed, or
// input-pending call can never commit a declaration. The returned names must be
// bound deferred Tools; a declaration of anything else is a Tool defect.
func (t *toolDispatcher) adviseDeferred(ctx context.Context, name string, invocation tool.Invocation, output tool.Result) (names []string, err error) {
	advertiser, found := t.advertisers[name]
	if !found {
		return nil, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			names = nil
			err = &run.CallbackPanicError{Operation: "AdvertisingTool.AdvertiseDeferred", Value: recovered}
		}
	}()
	declared, err := advertiser.AdvertiseDeferred(ctx, invocation, output)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToolAdvertisement, err)
	}
	if len(declared) == 0 {
		return nil, nil
	}
	aliases := make([]string, len(declared))
	for i, identity := range declared {
		alias := chat.SanitizeToolName(identity)
		item, exists := t.manifest.entries[alias]
		if !exists || item.contract.Name() != identity {
			return nil, fmt.Errorf("%w: unbound tool identity %q", ErrInvalidToolAdvertisement, identity)
		}
		aliases[i] = alias
	}
	canonical, err := t.manifest.normalizeAdvertisements(aliases)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToolAdvertisement, err)
	}
	return canonical, nil
}

func (t *toolDispatcher) prepareToolCall(call chat.ToolCall, continuation *tool.Continuation) preparedToolCall {
	prepared := preparedToolCall{call: call}
	binding, found := t.bindings[call.Name]
	if !found {
		result := rejectedToolResult(call, fmt.Sprintf("tool %q is not available", call.Name))
		prepared.rejection = &result
		return prepared
	}
	prepared.binding = binding
	invocation, err := t.manifest.entries[call.Name].prepare(call, continuation)
	if err != nil {
		if _, defect := errors.AsType[*tool.InputValidationPanicError](err); defect {
			prepared.defect = err
			return prepared
		}
		result := rejectedToolResult(call, "invalid arguments: "+run.NormalizeDiagnostic(err.Error()))
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
			err = &run.CallbackPanicError{Operation: "DirectResultTool.ReturnsDirectResult", Value: recovered}
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
			err = &run.CallbackPanicError{Operation: "ConcurrentTool.ConcurrencyPolicy", Value: recovered}
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
