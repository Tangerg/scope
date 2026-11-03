package interaction

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
	bindings    map[string]tool.Binding
	advertisers map[string]AdvertisingTool
	manifest    toolManifest
}

func newToolDispatcher() *toolDispatcher {
	return &toolDispatcher{
		bindings:    make(map[string]tool.Binding),
		advertisers: make(map[string]AdvertisingTool),
		manifest:    toolManifest{entries: make(map[string]toolManifestEntry)},
	}
}

func (t *toolDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	ctx = run.RequireContext(ctx)
	invocation, err := ToolInvocationFromRequest(request)
	if err != nil {
		return failedSettlement(err)
	}
	admission, err := invocation.admission()
	if err != nil {
		return failedSettlement(err)
	}
	if emit != nil {
		ctx = tool.WithProgressReporter(ctx, deltaProgress{emit: emit})
	}
	prepared := t.prepareToolCall(invocation.ToolCall(), admission)
	if prepared.defect != nil {
		// A validator defect is settled before Tool.Call runs, as a definite
		// failure that propagates to the parent, never a model-visible result.
		return failedSettlement(errToolInputValidatorDefect)
	}
	result, advertised, required, rejected, err := t.callTool(ctx, prepared)
	if err != nil && result.ID == "" {
		failure := toolUncertainty{Diagnostic: run.NormalizeDiagnostic(err.Error())}
		if evidence, ok := errors.AsType[*tool.CallError](err); ok && evidence.Validate() == nil {
			failure.Evidence = new(evidence.Evidence())
		}
		return (dispatchUncertainty{Tool: &failure}).settlement()
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

func (t *toolDispatcher) bindTool(executable tool.Tool, deferred bool, middlewares []tool.CallMiddleware) error {
	binding, err := tool.Bind(executable, middlewares...)
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
	direct, err := toolCapability(executable, "DirectResultTool.ReturnsDirectResult", DirectResultTool.ReturnsDirectResult)
	if err != nil {
		return err
	}
	concurrent, err := toolCapability(executable, "ConcurrentTool.ConcurrencyPolicy", ConcurrentTool.ConcurrencyPolicy)
	if err != nil {
		return err
	}
	advertiser, err := toolCapability(executable, "AdvertisingTool capability", func(advertiser AdvertisingTool) AdvertisingTool { return advertiser })
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
	binding := prepared.binding
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

func (t *toolDispatcher) prepareToolCall(call chat.ToolCall, admission tool.Admission) preparedToolCall {
	prepared := preparedToolCall{call: call}
	binding, found := t.bindings[call.Name]
	if !found {
		result := rejectedToolResult(call, fmt.Sprintf("tool %q is not available", call.Name))
		prepared.rejection = &result
		return prepared
	}
	prepared.binding = binding
	invocation, err := t.manifest.entries[call.Name].prepare(call, admission)
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

// toolCapability reads a consumer-owned declaration directly from the Tool. Declarations are read once at binding, so a panic there is a defect of
// the Tool that must reject construction rather than surface during recovery.
func toolCapability[C, V any](executable tool.Tool, declaration string, read func(C) V) (value V, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			var zero V
			value, err = zero, &run.CallbackPanicError{Operation: declaration, Value: recovered}
		}
	}()
	capability, found := executable.(C)
	if !found {
		return value, nil
	}
	return read(capability), nil
}
