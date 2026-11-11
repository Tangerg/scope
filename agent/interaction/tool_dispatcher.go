package interaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// Validator diagnostics may contain private details, so only a fixed defect
// category crosses into the run's failure.
var errToolInputValidatorDefect = errors.New("interaction: tool input validation failed")

type preparedToolCall struct {
	call       chat.ToolCall
	binding    tool.Binding
	invocation tool.Invocation
	rejection  *toolCallResult
	defect     error
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
	ctx = tool.WithProgressReporter(ctx, deltaProgress{emit: emit})
	prepared := t.prepareToolCall(invocation.ToolCall(), admission)
	if prepared.defect != nil {
		// A validator defect is settled before Tool.Call runs, as a definite
		// failure that propagates to the parent, never a model-visible result.
		return failedSettlement(errToolInputValidatorDefect)
	}
	outcome, err := t.callTool(ctx, prepared)
	if err != nil {
		failure := toolUncertainty{Diagnostic: run.NormalizeDiagnostic(err.Error())}
		if evidence, ok := errors.AsType[*tool.CallError](err); ok && evidence.Validate() == nil {
			failure.Evidence = new(evidence.Evidence())
		}
		return (dispatchUncertainty{Tool: &failure}).settlement()
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

// callTool returns the definite outcome of the prepared call. An error means
// the outcome is unknown; a completed call whose advertisement then fails keeps
// its result and records the failure beside it.
func (t *toolDispatcher) callTool(ctx context.Context, prepared preparedToolCall) (outcome toolDispatchResult, err error) {
	if prepared.rejection != nil {
		return toolDispatchResult{Completion: prepared.rejection}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome, err = toolDispatchResult{}, &run.CallbackPanicError{Operation: "Tool.Call", Value: recovered}
		}
	}()
	result, err := prepared.binding.Call(ctx, prepared.invocation)
	outcome, err = settleToolCall(result, err)
	if err != nil || outcome.Completion == nil || outcome.Completion.Disposition != ResultSucceeded {
		return outcome, err
	}
	names, err := t.adviseDeferred(ctx, prepared.call.Name, prepared.invocation, result)
	if err != nil {
		outcome.AdvertisementFailure = run.NormalizeDiagnostic(err.Error())
		return outcome, nil
	}
	outcome.Completion.AdvertisedToolNames = names
	return outcome, nil
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
	canonical, err := t.manifest.advertisedAliases(declared)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToolAdvertisement, err)
	}
	return canonical, nil
}

func (t *toolDispatcher) prepareToolCall(call chat.ToolCall, admission tool.Admission) preparedToolCall {
	prepared := preparedToolCall{call: call}
	binding, found := t.bindings[call.Name]
	if !found {
		prepared.rejection = new(rejectedCallResult(fmt.Sprintf("tool %q is not available", call.Name)))
		return prepared
	}
	prepared.binding = binding
	invocation, err := t.manifest.entries[call.Name].prepare(call, admission)
	if err != nil {
		rejection, public := tool.ClassifyInputRejection(err)
		if !public {
			prepared.defect = err
			return prepared
		}
		prepared.rejection = new(rejectedCallResult("invalid arguments: " + run.NormalizeDiagnostic(rejection.Message())))
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
