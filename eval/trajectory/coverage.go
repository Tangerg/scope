package trajectory

import (
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
)

// Coverage is the Host's exhaustive classification of dispatcher deployments
// in this recording. Models and Tools require semantic observations for every
// dispatched effect. Other deployments assert that they perform neither kind
// of call. Nil coverage leaves semantic completeness unknown; an empty value
// asserts that the tree performs no dispatcher effects.
// The Host owns classification of custom strategies and observer installation.
type Coverage struct {
	Models []agent.DeploymentRef `json:"models,omitempty"`
	Tools  []agent.DeploymentRef `json:"tools,omitempty"`
	Other  []agent.DeploymentRef `json:"other,omitempty"`
}

func (c *Coverage) clone() *Coverage {
	if c == nil {
		return nil
	}
	return &Coverage{Models: slices.Clone(c.Models), Tools: slices.Clone(c.Tools), Other: slices.Clone(c.Other)}
}

func (c Coverage) Validate() error {
	seen := make(map[agent.DeploymentRef]bool)
	for _, group := range [][]agent.DeploymentRef{c.Models, c.Tools, c.Other} {
		for _, reference := range group {
			if !reference.Valid() || seen[reference] {
				return fmt.Errorf("%w: coverage requires unique valid deployments", ErrInvalidTrajectory)
			}
			seen[reference] = true
		}
	}
	return nil
}
