package trajectory

import (
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/run"
)

// Coverage declares the protocol of each dispatcher intent independently of
// its result. Only declared interaction effects are decoded as model or tool
// calls; arbitrary dispatcher payloads remain opaque.
type Coverage struct {
	Models []run.EffectID `json:"models,omitempty"`
	Tools  []run.EffectID `json:"tools,omitempty"`
	Other  []run.EffectID `json:"other,omitempty"`
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

func (c Coverage) classifications() (map[run.EffectID]effectRole, error) {
	seen := make(map[run.EffectID]effectRole, len(c.Models)+len(c.Tools)+len(c.Other))
	for _, group := range [...]struct {
		role    effectRole
		effects []run.EffectID
	}{
		{effectRoleModel, c.Models}, {effectRoleTool, c.Tools}, {effectRoleOther, c.Other},
	} {
		for _, id := range group.effects {
			if !id.Valid() || seen[id] != effectRoleInvalid {
				return nil, fmt.Errorf("%w: coverage requires unique valid effect identities", ErrInvalidTrajectory)
			}
			seen[id] = group.role
		}
	}
	return seen, nil
}

type effectRole uint8

const (
	effectRoleInvalid effectRole = iota
	effectRoleModel
	effectRoleTool
	effectRoleOther
)
