package trajectory

import (
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
)

// EffectReference identifies one dispatcher Effect in one runtime activation.
// TreeIncarnationID is zero for an ephemeral runtime.
type EffectReference struct {
	ProcessID         agent.ProcessID         `json:"process_id"`
	TreeIncarnationID agent.TreeIncarnationID `json:"tree_incarnation_id,omitzero"`
	EffectID          agent.EffectID          `json:"effect_id"`
}

func (e EffectReference) Valid() bool {
	return e.ProcessID.Valid() && e.EffectID.Valid() &&
		(e.TreeIncarnationID == (agent.TreeIncarnationID{}) || e.TreeIncarnationID.Valid())
}

// Coverage is the Host's exhaustive classification of dispatcher Effects in
// this recording. One Deployment may perform several kinds of operation.
// Models and Tools require exactly one semantic observation for each Effect;
// Other asserts that an Effect performs neither kind of call. The Host must
// classify requests independently of the observations being checked. Deriving
// coverage from recorded responses would conceal missing observations.
// Nil coverage leaves completeness unknown; an empty value asserts no dispatch.
type Coverage struct {
	Models []EffectReference `json:"models,omitempty"`
	Tools  []EffectReference `json:"tools,omitempty"`
	Other  []EffectReference `json:"other,omitempty"`
}

func (c *Coverage) clone() *Coverage {
	if c == nil {
		return nil
	}
	return &Coverage{Models: slices.Clone(c.Models), Tools: slices.Clone(c.Tools), Other: slices.Clone(c.Other)}
}

func (c Coverage) Validate() error {
	_, err := c.classifications()
	return err
}

type effectRole uint8

const (
	effectRoleInvalid effectRole = iota
	effectRoleModel
	effectRoleTool
	effectRoleOther
)

func (c Coverage) classifications() (map[EffectReference]effectRole, error) {
	seen := make(map[EffectReference]effectRole, len(c.Models)+len(c.Tools)+len(c.Other))
	for _, group := range []struct {
		role    effectRole
		effects []EffectReference
	}{
		{effectRoleModel, c.Models}, {effectRoleTool, c.Tools}, {effectRoleOther, c.Other},
	} {
		for _, reference := range group.effects {
			if !reference.Valid() || seen[reference] != effectRoleInvalid {
				return nil, fmt.Errorf("%w: coverage requires unique valid Effect references", ErrInvalidTrajectory)
			}
			seen[reference] = group.role
		}
	}
	return seen, nil
}
