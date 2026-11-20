package trajectory

import (
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/run"
)

// Coverage declares the protocol each Program speaks. Every dispatcher Effect
// of a process that runs an Interactions Program is decoded as a model or tool
// call; the Effects of an Opaque Program are never interpreted. The Program
// that emits an Effect owns its protocol, so the declaration names Programs
// rather than Effects and cannot disagree with a payload.
type Coverage struct {
	Interactions []run.ProgramRef `json:"interactions,omitempty"`
	Opaque       []run.ProgramRef `json:"opaque,omitempty"`
}

func (c *Coverage) clone() *Coverage {
	if c == nil {
		return nil
	}
	return &Coverage{Interactions: slices.Clone(c.Interactions), Opaque: slices.Clone(c.Opaque)}
}

func (c Coverage) Validate() error {
	_, err := c.protocols()
	return err
}

func (c Coverage) protocols() (map[run.ProgramRef]protocol, error) {
	protocols := make(map[run.ProgramRef]protocol, len(c.Interactions)+len(c.Opaque))
	for _, group := range [...]struct {
		protocol protocol
		programs []run.ProgramRef
	}{
		{protocolInteraction, c.Interactions}, {protocolOpaque, c.Opaque},
	} {
		for _, program := range group.programs {
			if !program.Valid() || protocols[program] != protocolUndeclared {
				return nil, fmt.Errorf("%w: coverage requires unique valid Program references", ErrInvalidTrajectory)
			}
			protocols[program] = group.protocol
		}
	}
	return protocols, nil
}

type protocol uint8

const (
	protocolUndeclared protocol = iota
	protocolInteraction
	protocolOpaque
)
