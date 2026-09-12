package workflow

import (
	agent "github.com/Tangerg/scope/agent"
)

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
