package interaction

import (
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// ToolOutcome classifies one committed tool call. Every projection of a tool
// settlement reads it rather than re-deriving it from the settlement's parts.
// The outcomes of a result the model received are its ResultDisposition.
type ToolOutcome string

const (
	ToolOutcomeInvalid       ToolOutcome = ""
	ToolOutcomeSucceeded                 = ToolOutcome(ResultSucceeded)
	ToolOutcomeFailed                    = ToolOutcome(ResultFailed)
	ToolOutcomeRejected                  = ToolOutcome(ResultRejected)
	ToolOutcomeInputRequired ToolOutcome = "input_required"
	// ToolOutcomeDispatchFailed means dispatch failed definitely; no result
	// reached the model.
	ToolOutcomeDispatchFailed ToolOutcome = "dispatch_failed"
	ToolOutcomeUnknown        ToolOutcome = "unknown"
)

func (t ToolOutcome) Valid() bool {
	switch t {
	case ToolOutcomeSucceeded, ToolOutcomeFailed, ToolOutcomeRejected, ToolOutcomeInputRequired, ToolOutcomeDispatchFailed, ToolOutcomeUnknown:
		return true
	default:
		return false
	}
}

// ToolSettlement is a detached projection of a committed tool Effect.
type ToolSettlement struct {
	Outcome  ToolOutcome      `json:"outcome"`
	Result   *chat.ToolResult `json:"result,omitzero"`
	Failure  string           `json:"failure,omitzero"`
	Evidence *tool.Output     `json:"evidence,omitzero"`
}
