package interaction

import (
	"fmt"

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
	Observer             ToolObserver
	ImplementationDigest agent.Digest
	ConfigurationDigest  agent.Digest
}

// ToolSet binds an immutable Tool manifest to one Deployment that executes a
// single call per child Process. Definition owns scheduling; the child owns its
// Effect settlement and any input continuation. No model client enters this
// binding. A zero ToolSet represents the absence of ordinary Tools.
type ToolSet struct {
	deployment agent.Deployment
	manifest   toolManifest
	dispatcher *toolDispatcher
}

// NewToolSet freezes Tools and composes the canonical Definition,
// Dispatcher, and Deployment contracts into a callable Tool collection.
func NewToolSet(config ToolSetConfig) (ToolSet, error) {
	if len(config.Tools)+len(config.DeferredTools) == 0 {
		return ToolSet{}, fmt.Errorf("%w: at least one Tool is required", ErrInvalidToolSet)
	}
	if config.Observer != nil && lo.IsNil(config.Observer) {
		return ToolSet{}, fmt.Errorf("%w: Observer is typed nil", ErrInvalidToolSet)
	}
	dispatcher := &toolDispatcher{
		tools: make(map[string]boundTool), deferredToolNames: make(map[string]struct{}), observer: config.Observer,
	}
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
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: config.ImplementationDigest, ConfigurationDigest: config.ConfigurationDigest,
	})
	if err != nil {
		return ToolSet{}, fmt.Errorf("%w: %w", ErrInvalidToolSet, err)
	}
	manifest := toolManifest{
		deploymentRef:      deployment.DeploymentRef(),
		initialDefinitions: cloneDefinitions(dispatcher.initialDefinitions),
		entries:            make(map[string]toolManifestEntry, len(dispatcher.tools)),
	}
	for name, binding := range dispatcher.tools {
		manifest.entries[name] = toolManifestEntry{
			contract: binding.executable.Contract(), deferred: binding.deferred,
			concurrent: binding.concurrent,
		}
	}
	return ToolSet{deployment: deployment, manifest: manifest, dispatcher: dispatcher}, nil
}

// Deployment returns the exact child binding to include in the Engine's
// DeploymentResolver alongside any other explicitly referenced children.
func (t ToolSet) Deployment() agent.Deployment { return t.deployment }

func (t ToolSet) Valid() bool {
	return t.deployment.Valid() && t.dispatcher != nil &&
		t.manifest.deploymentRef == t.deployment.DeploymentRef() && len(t.manifest.entries) > 0
}

// ObservationFailures returns isolated Tool observer panic counts.
func (t ToolSet) ObservationFailures() ObservationFailureCounts {
	if t.dispatcher == nil {
		return ObservationFailureCounts{}
	}
	return t.dispatcher.observationFailures.snapshot()
}

type toolManifest struct {
	deploymentRef      agent.DeploymentRef
	initialDefinitions []chat.ToolDefinition
	entries            map[string]toolManifestEntry
}

type toolManifestEntry struct {
	contract   tool.Contract
	deferred   bool
	concurrent func(tool.Invocation) (string, bool)
}
