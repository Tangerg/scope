package chat

import (
	"errors"
	"fmt"
)

var ErrInvalidToolChoice = errors.New("chat: invalid tool choice")

type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNamed    ToolChoiceMode = "named"
)

func (t ToolChoiceMode) Valid() bool {
	switch t {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired, ToolChoiceNamed:
		return true
	default:
		return false
	}
}

// ToolParallelism states whether one response may request multiple calls; the
// zero value delegates the decision to the provider.
type ToolParallelism string

const (
	ToolParallelismAllow  ToolParallelism = "allow"
	ToolParallelismSingle ToolParallelism = "single"
)

func (t ToolParallelism) Valid() bool {
	return t == "" || t == ToolParallelismAllow || t == ToolParallelismSingle
}

type ToolChoice struct {
	Mode        ToolChoiceMode  `json:"mode"`
	Name        string          `json:"name,omitempty"`
	Parallelism ToolParallelism `json:"parallelism,omitempty"`
}

func (t *ToolChoice) Clone() *ToolChoice {
	if t == nil {
		return nil
	}
	clone := *t
	return &clone
}

func (t ToolChoice) Validate() error {
	if !t.Mode.Valid() {
		return fmt.Errorf("%w: unknown mode %q", ErrInvalidToolChoice, t.Mode)
	}
	if !t.Parallelism.Valid() {
		return fmt.Errorf("%w: unknown parallelism %q", ErrInvalidToolChoice, t.Parallelism)
	}
	if t.Mode == ToolChoiceNamed {
		if !toolNamePattern.MatchString(t.Name) {
			return fmt.Errorf("%w: name must match %s", ErrInvalidToolChoice, toolNamePattern)
		}
	} else if t.Name != "" {
		return fmt.Errorf("%w: mode %q cannot name a tool", ErrInvalidToolChoice, t.Mode)
	}
	if t.Mode == ToolChoiceNone && t.Parallelism != "" {
		return fmt.Errorf("%w: mode %q cannot set parallelism", ErrInvalidToolChoice, t.Mode)
	}
	return nil
}
