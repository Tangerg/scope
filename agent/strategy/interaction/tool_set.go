package interaction

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
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
	DecorateDispatcher   func(agent.Dispatcher) (agent.Dispatcher, error)
	ImplementationDigest agent.Digest
	ConfigurationDigest  agent.Digest
}

// ToolSet binds an immutable Tool manifest to one Deployment that executes a
// single call per child Process. Definition owns scheduling; the child owns its
// Effect settlement and any input continuation. No model client enters this
// binding. A zero ToolSet represents the absence of ordinary Tools.
type ToolSet struct {
	deployment agent.Deployment
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

func (c ToolSetConfig) validate() error {
	switch {
	case len(c.Tools)+len(c.DeferredTools) == 0:
		return fmt.Errorf("%w: at least one Tool is required", ErrInvalidToolSet)
	case c.Observer != nil && lo.IsNil(c.Observer):
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
	deployed := agent.Dispatcher(dispatcher)
	if config.DecorateDispatcher != nil {
		deployed, err = config.DecorateDispatcher(dispatcher)
		if err != nil {
			return ToolSet{}, fmt.Errorf("%w: decorate dispatcher: %w", ErrInvalidToolSet, err)
		}
		if lo.IsNil(deployed) {
			return ToolSet{}, fmt.Errorf("%w: DecorateDispatcher returned a nil Dispatcher", ErrInvalidToolSet)
		}
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: deployed,
		ImplementationDigest: config.ImplementationDigest, ConfigurationDigest: config.ConfigurationDigest,
	})
	if err != nil {
		return ToolSet{}, fmt.Errorf("%w: %w", ErrInvalidToolSet, err)
	}
	return ToolSet{deployment: deployment, dispatcher: dispatcher}, nil
}

// Deployment returns the exact child binding each Tool call runs under. An
// Interaction configured with this ToolSet owns it as one of its
// ChildDeployments.
func (t ToolSet) Deployment() agent.Deployment { return t.deployment }

// SettleToolResult converts an investigated external outcome into the same
// completion protocol used by live dispatch, without calling or replaying the
// Tool. request must be the original Engine-minted request for this exact
// Deployment; its call identifies the result, and disposition alone decides
// whether the output reports an error. Direct-return policy comes only from
// the binding; rejected and failed results cannot advertise Tools.
func (t ToolSet) SettleToolResult(request agent.EffectRequest, output chat.ToolOutput, disposition ResultDisposition, advertisedToolNames []string) (agent.Settlement, error) {
	if !t.Configured() || !request.Valid() || request.DeploymentRef() != t.deployment.DeploymentRef() {
		return agent.Settlement{}, fmt.Errorf("%w: Tool recovery requires its exact Deployment request", ErrInvalidProtocol)
	}
	if !disposition.Valid() {
		return agent.Settlement{}, fmt.Errorf("%w: Tool recovery disposition is invalid", ErrInvalidProtocol)
	}
	invocation, err := ToolInvocationFromRequest(request)
	if err != nil {
		return agent.Settlement{}, err
	}
	call := invocation.ToolCall()
	prepared := t.dispatcher.prepareToolCall(call, nil)
	if prepared.defect != nil {
		return agent.Settlement{}, fmt.Errorf("%w: Tool recovery input validation failed: %w", ErrInvalidProtocol, prepared.defect)
	}
	if prepared.rejection != nil {
		return agent.Settlement{}, fmt.Errorf("%w: Tool recovery call is not admitted by its binding", ErrInvalidProtocol)
	}
	if err := t.manifest().validateAdvertisements(advertisedToolNames); err != nil {
		return agent.Settlement{}, fmt.Errorf("%w: Tool recovery advertisements: %w", ErrInvalidProtocol, err)
	}
	result := chat.ToolResult{ID: call.ID, Name: call.Name, Output: output, IsError: disposition != ResultSucceeded}
	completion := prepared.completion(result, disposition == ResultRejected, advertisedToolNames)
	if err := completion.validateCall(call); err != nil {
		return agent.Settlement{}, err
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
	initialDefinitions []chat.ToolDefinition
	entries            map[string]toolManifestEntry
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
	merged := slices.Clone(current)
	seen := make(map[string]struct{}, len(current))
	for _, name := range current {
		seen[name] = struct{}{}
	}
	for _, name := range additions {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	return merged, nil
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

type toolConcurrencyPlan struct {
	concurrent bool
	key        string
}

func (t toolManifestEntry) plan(call chat.ToolCall) (plan toolConcurrencyPlan, err error) {
	if t.concurrent == nil {
		return toolConcurrencyPlan{}, nil
	}
	invocation, err := t.contract.Prepare(call, nil)
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
			plan, err = toolConcurrencyPlan{}, &agent.CallbackPanicError{Operation: "ConcurrentTool policy", Value: recovered}
		}
	}()
	key, concurrent := t.concurrent(invocation)
	return toolConcurrencyPlan{concurrent: concurrent, key: key}, nil
}
