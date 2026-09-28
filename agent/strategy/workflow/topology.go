package workflow

import (
	agent "github.com/Tangerg/scope/agent"
)

type BindingRole string

const (
	BindingRoleInvalid BindingRole = ""
	BindingRoleCall    BindingRole = "call"
	BindingRoleCase    BindingRole = "case"
	BindingRoleBranch  BindingRole = "branch"
	BindingRoleItem    BindingRole = "item"
	BindingRoleBody    BindingRole = "body"
)

func (b BindingRole) Valid() bool {
	return b == BindingRoleCall || b == BindingRoleCase || b == BindingRoleBranch || b == BindingRoleItem || b == BindingRoleBody
}

func (b BindingRole) String() string {
	if !b.Valid() {
		return invalidEnumName
	}
	return string(b)
}

// BindingTopology is a function-free projection of one exact child binding.
// ID is present only for named Switch cases and Fork branches.
type BindingTopology struct {
	Role          BindingRole         `json:"role"`
	ID            string              `json:"id,omitempty"`
	DeploymentRef agent.DeploymentRef `json:"deployment_ref"`
	InputSchema   agent.Schema        `json:"input_schema"`
	OutputSchema  agent.Schema        `json:"output_schema"`
	// Budget is the non-renewable allocation for each child start.
	Budget       agent.Budget        `json:"budget"`
	Capabilities agent.CapabilitySet `json:"capabilities"`
}

// StageTopology is a function-free projection of one sealed Stage. Limits are
// present only for the Stage kinds that own them.
type StageTopology struct {
	ID           string       `json:"id"`
	Kind         StageKind    `json:"kind"`
	InputSchema  agent.Schema `json:"input_schema"`
	OutputSchema agent.Schema `json:"output_schema"`
	// Bindings are exact child bindings in stable declaration order.
	Bindings []BindingTopology `json:"bindings,omitempty"`
	// WindowSize is the fixed Fork or Map execution-window size.
	WindowSize uint32 `json:"window_size,omitzero"`
	// MaxItems is the maximum accepted Map input length.
	MaxItems uint32 `json:"max_items,omitzero"`
	// MaxIterations is present only for a Loop, including an unlimited Loop.
	MaxIterations *agent.Quota `json:"max_iterations,omitzero"`
}

// Topology is a detached Definition-derived, function-free projection for
// diagnostics, documentation, UI rendering, and deployment audit. Mutating a
// projection never changes the Definition or a later projection.
type Topology struct {
	Descriptor agent.Descriptor `json:"descriptor"`
	// Stages are projected in execution order.
	Stages []StageTopology `json:"stages"`
}
