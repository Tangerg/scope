package trajectory

import (
	"fmt"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

type ToolOutcome string

const (
	ToolOutcomeInvalid       ToolOutcome = ""
	ToolOutcomePending       ToolOutcome = "pending"
	ToolOutcomeSucceeded     ToolOutcome = "succeeded"
	ToolOutcomeError         ToolOutcome = "error"
	ToolOutcomeInputRequired ToolOutcome = "input_required"
	ToolOutcomeFailed        ToolOutcome = "failed"
	ToolOutcomeUnknown       ToolOutcome = "unknown"
)

func (t ToolOutcome) Valid() bool {
	switch t {
	case ToolOutcomePending, ToolOutcomeSucceeded, ToolOutcomeError, ToolOutcomeInputRequired, ToolOutcomeFailed, ToolOutcomeUnknown:
		return true
	default:
		return false
	}
}

// These detached projections cannot advance a call. The intent and latest
// settlement in Trajectory.Log remain its only persisted representation.
type ModelCall struct {
	EffectID   run.EffectID                 `json:"effect_id"`
	Request    *chat.Request                `json:"request"`
	Settlement *interaction.ModelSettlement `json:"settlement,omitzero"`
}

func (m ModelCall) Clone() ModelCall {
	m.Request = m.Request.Clone()
	if m.Settlement != nil {
		settlement := *m.Settlement
		settlement.Response = settlement.Response.Clone()
		m.Settlement = &settlement
	}
	return m
}
func (m ModelCall) toolDecisions() []chat.ToolCall {
	if m.Settlement == nil || m.Settlement.Response == nil || m.Settlement.Response.Output.Message == nil {
		return nil
	}
	var calls []chat.ToolCall
	for _, part := range m.Settlement.Response.Output.Message.Parts {
		if part.Kind == chat.PartToolCall {
			calls = append(calls, *part.ToolCall)
		}
	}
	return calls
}

type ToolCall struct {
	EffectID       run.EffectID                `json:"effect_id"`
	ContinuationOf run.EffectID                `json:"continuation_of,omitzero"`
	Call           chat.ToolCall               `json:"call"`
	Status         run.SettlementStatus        `json:"status,omitzero"`
	Settlement     *interaction.ToolSettlement `json:"settlement,omitzero"`
}

func (t ToolCall) Clone() ToolCall {
	if t.Settlement != nil {
		settlement := *t.Settlement
		if settlement.Result != nil {
			result := settlement.Result.Clone()
			settlement.Result = &result
		}
		if settlement.Evidence != nil {
			evidence := settlement.Evidence.Clone()
			settlement.Evidence = &evidence
		}
		t.Settlement = &settlement
	}
	return t
}
func (t ToolCall) Outcome() ToolOutcome {
	if t.Settlement == nil {
		return ToolOutcomePending
	}
	switch t.Status {
	case run.SettlementStatusUnknown:
		return ToolOutcomeUnknown
	case run.SettlementStatusFailed:
		return ToolOutcomeFailed
	case run.SettlementStatusSucceeded:
		if t.Settlement.InputRequired {
			return ToolOutcomeInputRequired
		}
		if t.Settlement.Result == nil {
			return ToolOutcomeInvalid
		}
		if t.Settlement.Result.IsError {
			return ToolOutcomeError
		}
		return ToolOutcomeSucceeded
	default:
		return ToolOutcomeInvalid
	}
}

type callProjection struct {
	models map[run.EffectID]ModelCall
	tools  map[run.EffectID]ToolCall
}

func projectCalls(log run.Log, coverage *Coverage) (callProjection, error) {
	projection := callProjection{models: make(map[run.EffectID]ModelCall), tools: make(map[run.EffectID]ToolCall)}
	if coverage == nil {
		return projection, nil
	}
	roles, err := coverage.classifications()
	if err != nil {
		return callProjection{}, err
	}
	seen := make(map[run.EffectID]bool, len(roles))
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.EffectIntent:
			role := roles[record.ID]
			if role == effectRoleInvalid {
				continue
			}
			if record.Effect.Target() != run.EffectTargetDispatcher {
				return callProjection{}, fmt.Errorf("%w: coverage names a kernel effect", ErrInvalidTrajectory)
			}
			seen[record.ID] = true
			if err := projection.intent(record, role); err != nil {
				return callProjection{}, err
			}
		case run.EffectSettled:
			if err := projection.settle(record); err != nil {
				return callProjection{}, err
			}
		}
	}
	if len(seen) != len(roles) {
		return callProjection{}, fmt.Errorf("%w: coverage names an absent effect", ErrInvalidTrajectory)
	}
	return projection, nil
}
func (c *callProjection) intent(record run.EffectIntent, role effectRole) error {
	switch role {
	case effectRoleModel:
		request, recognized, err := interaction.ModelRequestFromEffect(record.Effect)
		if err != nil {
			return fmt.Errorf("%w: model intent %s: %w", ErrInvalidTrajectory, record.ID, err)
		}
		if !recognized {
			return fmt.Errorf("%w: model intent %s has a different protocol", ErrInvalidTrajectory, record.ID)
		}
		c.models[record.ID] = ModelCall{EffectID: record.ID, Request: request.Clone()}
	case effectRoleTool:
		call, reference, recognized, err := interaction.ToolCallFromEffect(record.Effect)
		if err != nil {
			return fmt.Errorf("%w: tool intent %s: %w", ErrInvalidTrajectory, record.ID, err)
		}
		if !recognized {
			return fmt.Errorf("%w: tool intent %s has a different protocol", ErrInvalidTrajectory, record.ID)
		}
		c.tools[record.ID] = ToolCall{EffectID: record.ID, ContinuationOf: reference, Call: call}
	}
	return nil
}
func (c *callProjection) settle(record run.EffectSettled) error {
	if call, ok := c.models[record.ID]; ok {
		settlement := interaction.ModelSettlement{Outcome: interaction.ModelOutcomeUnknown}
		if record.Failure != nil {
			settlement.Failure = record.Failure.Message()
		} else {
			var err error
			settlement, err = interaction.ModelSettlementFromOutcome(record.Outcome)
			if err != nil {
				return fmt.Errorf("%w: model settlement %s: %w", ErrInvalidTrajectory, record.ID, err)
			}
		}
		if err := settlement.Validate(); err != nil {
			return fmt.Errorf("%w: model settlement %s: %w", ErrInvalidTrajectory, record.ID, err)
		}
		call.Settlement = &settlement
		c.models[record.ID] = call
	}
	if call, ok := c.tools[record.ID]; ok {
		var settlement interaction.ToolSettlement
		if record.Failure != nil {
			settlement.Failure = record.Failure.Message()
		} else {
			var err error
			settlement, err = interaction.ToolSettlementFromOutcome(call.Call, record.Outcome)
			if err != nil {
				return fmt.Errorf("%w: tool settlement %s: %w", ErrInvalidTrajectory, record.ID, err)
			}
		}
		call.Status, call.Settlement = record.Outcome.Status(), &settlement
		if !call.Outcome().Valid() {
			return fmt.Errorf("%w: invalid tool settlement %s", ErrInvalidTrajectory, record.ID)
		}
		c.tools[record.ID] = call
	}
	return nil
}
