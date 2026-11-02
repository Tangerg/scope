package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// ToolSetConfig freezes ordinary Tool authority and its exact execution binding.
// The digests cover the executable Tools, optional capabilities, and observer.
type ToolSetConfig struct {
	Name          string
	Description   string
	Tools         []tool.Tool
	DeferredTools []tool.Tool
	// Observer receives exact Tool call facts. Nil disables observation.
	Observer ToolObserver
	// DecorateDispatcher optionally wraps the Tool child's Dispatcher once at
	// construction, reaching the full Dispatch boundary that ToolObserver and
	// per-Tool wrappers cannot. The wrapper must delegate and preserve the
	// Settlement, Policy, and replay semantics; SettleToolResult recovery uses the
	// undecorated Dispatcher, and the Host folds the decoration into the digests.
	DecorateDispatcher   func(run.Dispatcher) (run.Dispatcher, error)
	ImplementationDigest run.Digest
	ConfigurationDigest  run.Digest
}

// ToolSet binds an immutable Tool manifest to one Program that executes a
// single call per child Process. Definition owns scheduling; the child owns its
// Effect settlement and any input continuation. No model client enters this
// binding. A zero ToolSet represents the absence of ordinary Tools.
type ToolSet struct {
	deployment run.Program[run.Payload, run.Payload]
	dispatcher *toolDispatcher
}

// manifest is the scheduling policy the Tool dispatcher bound; a zero ToolSet
// has none.
func (t ToolSet) manifest() toolManifest {
	if t.dispatcher == nil {
		return toolManifest{}
	}
	return t.dispatcher.manifest
}

func (t ToolSetConfig) validate() error {
	switch {
	case !t.ImplementationDigest.Valid() || !t.ConfigurationDigest.Valid():
		return fmt.Errorf("%w: implementation and configuration: %w", ErrInvalidToolSet, run.ErrInvalidDigest)
	case len(t.Tools)+len(t.DeferredTools) == 0:
		return fmt.Errorf("%w: at least one Tool is required", ErrInvalidToolSet)
	case t.Observer != nil && lo.IsNil(t.Observer):
		return fmt.Errorf("%w: Observer is typed nil", ErrInvalidToolSet)
	}
	return nil
}

func NewToolSet(config ToolSetConfig) (ToolSet, error) {
	if err := config.validate(); err != nil {
		return ToolSet{}, err
	}
	dispatcher := newToolDispatcher(config.Observer)
	for index, executable := range config.Tools {
		if err := dispatcher.bindTool(executable, false); err != nil {
			return ToolSet{}, fmt.Errorf("%w: Tools[%d]: %w", ErrInvalidToolSet, index, err)
		}
	}
	for index, executable := range config.DeferredTools {
		if err := dispatcher.bindTool(executable, true); err != nil {
			return ToolSet{}, fmt.Errorf("%w: DeferredTools[%d]: %w", ErrInvalidToolSet, index, err)
		}
	}
	definition, err := newToolDefinition(config.Name, config.Description)
	if err != nil {
		return ToolSet{}, fmt.Errorf("%w: %w", ErrInvalidToolSet, err)
	}
	// The ToolSet keeps the original Dispatcher so manifest scheduling and
	// SettleToolResult recovery bypass any decorator.
	deployed := run.Dispatcher(dispatcher)
	if config.DecorateDispatcher != nil {
		deployed, err = config.DecorateDispatcher(dispatcher)
		if err != nil {
			return ToolSet{}, fmt.Errorf("%w: decorate dispatcher: %w", ErrInvalidToolSet, err)
		}
		if lo.IsNil(deployed) {
			return ToolSet{}, fmt.Errorf("%w: DecorateDispatcher returned a nil Dispatcher", ErrInvalidToolSet)
		}
	}
	type entryFact struct {
		Definition                               tool.Definition
		Deferred, Direct, Concurrent, Advertises bool
	}
	entries := make(map[string]entryFact, len(dispatcher.manifest.entries))
	for name, entry := range dispatcher.manifest.entries {
		entries[name] = entryFact{entry.contract.Definition(), entry.deferred, entry.direct, entry.concurrent != nil, dispatcher.advertisers[name] != nil}
	}
	encoded, err := jsonv2.Marshal(struct {
		Opaque  run.Digest
		Visible []tool.Definition
		Entries map[string]entryFact
	}{config.ConfigurationDigest, dispatcher.manifest.initialDefinitions(), entries}, jsonv2.Deterministic(true))
	if err != nil {
		return ToolSet{}, fmt.Errorf("%w: freeze tool configuration: %w", ErrInvalidToolSet, err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: deployed,
		ImplementationDigest: config.ImplementationDigest, ConfigurationDigest: run.ComputeDigest(encoded),
	})
	if err != nil {
		return ToolSet{}, fmt.Errorf("%w: %w", ErrInvalidToolSet, err)
	}
	return ToolSet{deployment: deployment, dispatcher: dispatcher}, nil
}

// Program returns the exact child binding each Tool call runs under. An
// Interaction configured with this ToolSet owns it as one of its
// ChildPrograms.
func (t ToolSet) Program() run.Program[run.Payload, run.Payload] { return t.deployment }

// SettleToolResult converts an investigated external outcome into the same
// completion protocol used by live dispatch, without calling or replaying the
// Tool. request must be the original Runtime-minted request for this exact
// Program; its call identifies the result, and disposition alone decides
// whether the output reports an error. Direct-return policy comes only from
// the binding; rejected and failed results cannot advertise Tools.
func (t ToolSet) SettleToolResult(request run.EffectRequest, output tool.Output, disposition ResultDisposition, advertisedToolNames []string) (run.Settlement, error) {
	if !t.Configured() || !request.Valid() || request.ProgramRef() != t.deployment.ProgramRef() {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery requires its exact Program request", ErrInvalidProtocol)
	}
	if !disposition.Valid() {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery disposition is invalid", ErrInvalidProtocol)
	}
	invocation, err := ToolInvocationFromRequest(request)
	if err != nil {
		return run.Settlement{}, err
	}
	call := invocation.ToolCall()
	prepared := t.dispatcher.prepareToolCall(call, nil)
	if prepared.defect != nil {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery input validation failed: %w", ErrInvalidProtocol, prepared.defect)
	}
	if prepared.rejection != nil {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery call is not admitted by its binding", ErrInvalidProtocol)
	}
	canonical, err := t.manifest().normalizeAdvertisements(advertisedToolNames)
	if err != nil {
		return run.Settlement{}, fmt.Errorf("%w: Tool recovery advertisements: %w", ErrInvalidProtocol, err)
	}
	result := chat.ToolResult{ID: call.ID, Name: call.Name, Output: output, IsError: disposition != ResultSucceeded}
	completion := prepared.completion(result, disposition == ResultRejected, canonical)
	if err := completion.validateCall(call); err != nil {
		return run.Settlement{}, err
	}
	return (toolDispatchResult{Completion: completion}).settlement()
}

// Configured distinguishes a constructed ToolSet from the optional absence of Tools.
func (t ToolSet) Configured() bool { return t.dispatcher != nil }

// ObservationFailures returns isolated Tool observer panic counts and diagnostics.
func (t ToolSet) ObservationFailures() ObservationFailures {
	if t.dispatcher == nil {
		return ObservationFailures{}
	}
	return t.dispatcher.observationFailures.snapshot()
}

// toolManifest is the scheduling policy an Interaction retains: Tool
// contracts and frozen classifiers, never the executable Tools.
type toolManifest struct {
	// Preserve configured Tool order; entries owns every projected definition.
	initialNames []string
	entries      map[string]toolManifestEntry
}

func (t toolManifest) initialDefinitions() []tool.Definition {
	var definitions []tool.Definition
	for _, name := range t.initialNames {
		definitions = append(definitions, t.entries[name].modelDefinition())
	}
	return definitions
}

func (t toolManifest) deferred(name string) bool {
	return t.entries[name].deferred
}

func (t toolManifest) completesDirectly(name string) bool {
	return t.entries[name].direct
}

func (t toolManifest) mergeAdvertisements(current, additions []string) ([]string, error) {
	if err := t.validateAdvertisements(current); err != nil {
		return nil, err
	}
	if err := t.validateAdvertisements(additions); err != nil {
		return nil, err
	}
	merged := append(slices.Clone(current), additions...)
	slices.Sort(merged)
	return slices.Compact(merged), nil
}

// Live declarations describe a set; persisted state must already be canonical.
func (t toolManifest) normalizeAdvertisements(names []string) ([]string, error) {
	canonical := slices.Clone(names)
	slices.Sort(canonical)
	canonical = slices.Compact(canonical)
	if err := t.validateAdvertisements(canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

func (t toolManifest) validateAdvertisements(names []string) error {
	if err := validateAdvertisedToolNames(names); err != nil {
		return err
	}
	for _, name := range names {
		if !t.deferred(name) {
			return fmt.Errorf("tool %q is not a bound deferred Tool", name)
		}
	}
	return nil
}

// concurrentBatchEnd inspects only the next group and its first boundary.
// Classifying later calls again at every boundary makes exclusive runs quadratic.
func (t toolManifest) concurrentBatchEnd(ctx context.Context, calls []chat.ToolCall) (int, error) {
	claimed := make(map[string]struct{})
	for index, call := range calls {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entry, found := t.entries[call.Name]
		if !found {
			return index, nil
		}
		plan, err := entry.plan(call)
		if err != nil {
			return 0, fmt.Errorf("interaction: tool call %q concurrency: %w", call.ID, err)
		}
		if !plan.concurrent {
			if index == 0 {
				return 1, nil
			}
			return index, nil
		}
		if plan.key != "" {
			if _, duplicate := claimed[plan.key]; duplicate {
				return index, nil
			}
			claimed[plan.key] = struct{}{}
		}
	}
	return len(calls), nil
}

type toolManifestEntry struct {
	contract tool.Contract
	deferred bool
	// direct marks a Tool whose successful result completes the Interaction
	// without another model call.
	direct     bool
	concurrent func(tool.Invocation) (string, bool)
}

func (t toolManifestEntry) modelDefinition() tool.Definition {
	definition := t.contract.Definition()
	definition.Name = chat.SanitizeToolName(definition.Name)
	return definition
}

func (t toolManifestEntry) prepare(call chat.ToolCall, continuation *tool.Continuation) (tool.Invocation, error) {
	request := call.Request()
	request.Name = t.contract.Name()
	return t.contract.Prepare(request, continuation)
}

type toolConcurrencyPlan struct {
	concurrent bool
	key        string
}

func (t toolManifestEntry) plan(call chat.ToolCall) (plan toolConcurrencyPlan, err error) {
	if t.concurrent == nil {
		return toolConcurrencyPlan{}, nil
	}
	invocation, err := t.prepare(call, nil)
	if err != nil {
		if _, defect := errors.AsType[*tool.InputValidationPanicError](err); defect {
			// A validator defect is an internal fault, not a bad argument; fail the
			// Step rather than scheduling a call that cannot be validated.
			return toolConcurrencyPlan{}, errToolInputValidatorDefect
		}
		// Invalid arguments receive an ordinary rejection from the child; they
		// confer no authority to overlap other calls.
		return toolConcurrencyPlan{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			plan, err = toolConcurrencyPlan{}, &run.CallbackPanicError{Operation: "ConcurrentTool policy", Value: recovered}
		}
	}()
	key, concurrent := t.concurrent(invocation)
	return toolConcurrencyPlan{concurrent: concurrent, key: key}, nil
}
