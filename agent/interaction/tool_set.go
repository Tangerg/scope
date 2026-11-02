package interaction

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type ToolSetConfig struct {
	Revision      string
	Tools         []tool.Tool
	DeferredTools []tool.Tool
}

// ToolSet freezes execution and admission policy once; the interaction's
// dispatcher composes it without introducing a child process lifecycle.
type ToolSet struct {
	dispatcher *toolDispatcher
	revision   string
}

func (t ToolSet) manifest() toolManifest {
	if t.dispatcher == nil {
		return toolManifest{}
	}
	return t.dispatcher.manifest
}

func NewToolSet(config ToolSetConfig) (ToolSet, error) {
	if config.Revision == "" || len(config.Tools)+len(config.DeferredTools) == 0 {
		return ToolSet{}, fmt.Errorf("%w: Revision and at least one Tool are required", ErrInvalidToolSet)
	}
	dispatcher := newToolDispatcher()
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
	return ToolSet{dispatcher: dispatcher, revision: config.Revision}, nil
}

func (t ToolSet) Configured() bool { return t.dispatcher != nil }

func (t ToolSet) configuration() toolConfiguration {
	configuration := toolConfiguration{Revision: t.revision}
	for name, entry := range t.manifest().entries {
		configuration.Entries = append(configuration.Entries, toolConfigurationEntry{
			Name: name, Contract: entry.contract.Definition(), Deferred: entry.deferred,
			Direct: entry.direct, Idempotent: entry.idempotent, Concurrent: entry.concurrent != nil,
			Advertises: t.dispatcher.advertisers[name] != nil,
		})
	}
	slices.SortFunc(configuration.Entries, func(a, b toolConfigurationEntry) int { return strings.Compare(a.Name, b.Name) })
	configuration.VisibleOrder = slices.Clone(t.manifest().initialNames)
	return configuration
}

type toolConfiguration struct {
	Revision     string
	VisibleOrder []string
	Entries      []toolConfigurationEntry
}

type toolConfigurationEntry struct {
	Name                                                 string
	Contract                                             tool.Definition
	Deferred, Direct, Idempotent, Concurrent, Advertises bool
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
	idempotent bool
	concurrent func(tool.Invocation) (string, bool)
}

func (t toolManifestEntry) replayPolicy() run.ReplayPolicy {
	if t.idempotent {
		return run.ReplayPolicySameIdentity
	}
	return run.ReplayPolicyNever
}

func (t toolManifestEntry) modelDefinition() tool.Definition {
	definition := t.contract.Definition()
	definition.Name = chat.SanitizeToolName(definition.Name)
	return definition
}

func (t toolManifestEntry) prepare(call chat.ToolCall, admission tool.Admission) (tool.Invocation, error) {
	request := call.Request()
	request.Name = t.contract.Name()
	return t.contract.Prepare(request, admission)
}

type toolConcurrencyPlan struct {
	concurrent bool
	key        string
}

func (t toolManifestEntry) plan(call chat.ToolCall) (plan toolConcurrencyPlan, err error) {
	if t.concurrent == nil {
		return toolConcurrencyPlan{}, nil
	}
	invocation, err := t.prepare(call, tool.Admission{})
	if err != nil {
		if _, defect := errors.AsType[*tool.InputValidationPanicError](err); defect {
			// A validator defect is an internal fault, not a bad argument; fail the
			// Step rather than scheduling a call that cannot be validated.
			return toolConcurrencyPlan{}, errToolInputValidatorDefect
		}
		// Invalid arguments receive ordinary model-visible rejection; they
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
