package workflow

import agent "github.com/Tangerg/scope/agent"

// BindingRole describes how an exact child binding participates in a Stage.
type BindingRole string

const (
	// BindingRoleInvalid is the invalid zero value.
	BindingRoleInvalid BindingRole = ""
	// BindingRoleCall is the single child of a Call Stage.
	BindingRoleCall BindingRole = "call"
	// BindingRoleCase is one named child of a Switch Stage.
	BindingRoleCase BindingRole = "case"
	// BindingRoleBranch is one named child of a Fork Stage.
	BindingRoleBranch BindingRole = "branch"
	// BindingRoleItem is the repeated child of a Map Stage.
	BindingRoleItem BindingRole = "item"
	// BindingRoleBody is the repeated child of a Loop Stage.
	BindingRoleBody BindingRole = "body"
)

// BindingTopology is a function-free projection of one exact child binding.
// ID is present only for named Switch cases and Fork branches.
type BindingTopology struct {
	// Role is the binding's structural role in its Stage.
	Role BindingRole `json:"role"`
	// ID is the stable case or branch identity when the role is named.
	ID string `json:"id,omitempty"`
	// DeploymentRef is the exact child behavior binding identity.
	DeploymentRef agent.DeploymentRef `json:"deployment_ref"`
	// InputSchema is the exact input contract of the child binding.
	InputSchema agent.Schema `json:"input_schema"`
	// OutputSchema is the exact output contract of the child binding.
	OutputSchema agent.Schema `json:"output_schema"`
	// Budget is the non-renewable allocation for each child start.
	Budget agent.Budget `json:"budget"`
	// Capabilities is the attenuated authority granted to each child.
	Capabilities agent.CapabilitySet `json:"capabilities"`
}

// StageTopology is a function-free projection of one sealed Stage. Limits are
// non-zero only for the Stage kinds that own them.
type StageTopology struct {
	// ID is the stable Stage identity within the Definition.
	ID string `json:"id"`
	// Kind is the sealed operation kind.
	Kind StageKind `json:"kind"`
	// InputSchema is the exact Stage input contract.
	InputSchema agent.Schema `json:"input_schema"`
	// OutputSchema is the exact Stage output contract.
	OutputSchema agent.Schema `json:"output_schema"`
	// Bindings are exact child bindings in stable declaration order.
	Bindings []BindingTopology `json:"bindings,omitempty"`
	// WindowSize is the fixed Fork or Map execution-window size.
	WindowSize uint32 `json:"window_size,omitempty"`
	// MaxItems is the maximum accepted Map input length.
	MaxItems uint32 `json:"max_items,omitempty"`
	// MaxIterations is the hard Loop body-start limit.
	MaxIterations uint32 `json:"max_iterations,omitempty"`
}

// Topology is a detached Definition-derived, function-free projection for
// diagnostics, documentation, UI rendering, and deployment audit. Mutating a
// projection never changes the Definition or a later projection.
type Topology struct {
	// Descriptor is the Workflow's authoritative static contract.
	Descriptor agent.Descriptor `json:"descriptor"`
	// Stages are projected in execution order.
	Stages []StageTopology `json:"stages"`
}

// Topology returns a fresh, function-free projection of this Definition. An
// invalid or nil Definition returns the zero Topology.
func (d *Definition) Topology() Topology {
	if !d.valid() {
		return Topology{}
	}
	stages := make([]StageTopology, len(d.stages))
	for index, stage := range d.stages {
		stages[index] = stage.topology()
	}
	return Topology{Descriptor: d.descriptor, Stages: stages}
}

func (s Stage) topology() StageTopology {
	projected := StageTopology{
		ID: s.id, Kind: s.kind,
		InputSchema: s.inputSchema, OutputSchema: s.outputSchema,
	}
	switch s.kind {
	case StageKindCall:
		projected.Bindings = []BindingTopology{
			s.call.topology(BindingRoleCall, "", s.inputSchema, s.outputSchema),
		}
	case StageKindSwitch:
		projected.Bindings = make([]BindingTopology, len(s.switcher.cases))
		for index, candidate := range s.switcher.cases {
			projected.Bindings[index] = candidate.binding.topology(
				BindingRoleCase, candidate.id, s.inputSchema, s.outputSchema,
			)
		}
	case StageKindFork:
		projected.WindowSize = s.fork.windowSize
		projected.Bindings = make([]BindingTopology, len(s.fork.branches))
		for index, branch := range s.fork.branches {
			projected.Bindings[index] = branch.binding.topology(
				BindingRoleBranch, branch.id, s.inputSchema, s.fork.branchSchema,
			)
		}
	case StageKindMap:
		projected.WindowSize = s.mapper.windowSize
		projected.MaxItems = s.mapper.maxItems
		projected.Bindings = []BindingTopology{s.mapper.binding.topology(
			BindingRoleItem, "",
			s.mapper.itemInputSchema, s.mapper.itemOutputSchema,
		)}
	case StageKindLoop:
		projected.MaxIterations = s.loop.maxIterations
		projected.Bindings = []BindingTopology{s.loop.binding.topology(
			BindingRoleBody, "", s.loop.valueSchema, s.loop.valueSchema,
		)}
	}
	return projected
}

func (c childBinding) topology(
	role BindingRole,
	id string,
	inputSchema agent.Schema,
	outputSchema agent.Schema,
) BindingTopology {
	return BindingTopology{
		Role: role, ID: id, DeploymentRef: c.deploymentRef,
		InputSchema: inputSchema, OutputSchema: outputSchema,
		Budget: c.budget, Capabilities: c.capabilities,
	}
}
