package trajectory

import (
	"fmt"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// These detached projections cannot advance a call. The intent and latest
// settlement in Trajectory.Log remain its only persisted representation.
type ModelCall struct {
	EffectID   run.EffectID                 `json:"effect_id"`
	Sequence   uint64                       `json:"sequence"`
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

// Outcome is the committed classification of the call, or ToolOutcomeInvalid
// while its intent awaits a settlement.
func (t ToolCall) Outcome() interaction.ToolOutcome {
	if t.Settlement == nil {
		return interaction.ToolOutcomeInvalid
	}
	return t.Settlement.Outcome
}

type callProjection struct {
	models map[run.EffectID]ModelCall
	tools  map[run.EffectID]ToolCall
	// undeclared is the first dispatcher Effect whose Program the Coverage does
	// not name, or zero when every one is declared.
	undeclared run.EffectID
}

func projectCalls(log run.Log, coverage *Coverage) (callProjection, error) {
	projection := callProjection{models: make(map[run.EffectID]ModelCall), tools: make(map[run.EffectID]ToolCall)}
	if coverage == nil {
		return projection, nil
	}
	protocols, err := coverage.protocols()
	if err != nil {
		return callProjection{}, err
	}
	programs := make(map[run.ProcessID]run.ProgramRef)
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Started:
			programs[record.Process] = record.Program
		case run.EffectIntent:
			if record.Effect.Target() != run.EffectTargetDispatcher {
				continue
			}
			if err := projection.intent(record, protocols[programs[record.ID.Process]]); err != nil {
				return callProjection{}, err
			}
		case run.EffectSettled:
			if err := projection.settle(record); err != nil {
				return callProjection{}, err
			}
		}
	}
	return projection, nil
}

// intent records a dispatcher Effect under the protocol of the Program that
// emitted it. An Interaction Program's context preparation stays opaque.
func (c *callProjection) intent(record run.EffectIntent, protocol protocol) error {
	switch protocol {
	case protocolUndeclared:
		if !c.undeclared.Valid() {
			c.undeclared = record.ID
		}
		return nil
	case protocolOpaque:
		return nil
	}
	model, isModel, err := interaction.ModelCallFromEffect(record.Effect)
	if err != nil {
		return fmt.Errorf("%w: interaction intent %s: %w", ErrInvalidTrajectory, record.ID, err)
	}
	if isModel {
		c.models[record.ID] = ModelCall{EffectID: record.ID, Sequence: model.Sequence, Request: model.Request.Clone()}
		return nil
	}
	call, isTool, err := interaction.ToolCallFromEffect(record.Effect)
	if err != nil {
		return fmt.Errorf("%w: interaction intent %s: %w", ErrInvalidTrajectory, record.ID, err)
	}
	if isTool {
		c.tools[record.ID] = ToolCall{EffectID: record.ID, ContinuationOf: call.ContinuationOf, Call: call.Call}
	}
	return nil
}

func (c *callProjection) settle(record run.EffectSettled) error {
	if call, ok := c.models[record.ID]; ok {
		settlement, err := interaction.ModelSettlementFromRecord(record)
		if err != nil {
			return fmt.Errorf("%w: model settlement %s: %w", ErrInvalidTrajectory, record.ID, err)
		}
		call.Settlement = &settlement
		c.models[record.ID] = call
	}
	if call, ok := c.tools[record.ID]; ok {
		settlement, err := interaction.ToolSettlementFromRecord(call.Call, record)
		if err != nil {
			return fmt.Errorf("%w: tool settlement %s: %w", ErrInvalidTrajectory, record.ID, err)
		}
		call.Settlement = &settlement
		if !call.Outcome().Valid() {
			return fmt.Errorf("%w: invalid tool settlement %s", ErrInvalidTrajectory, record.ID)
		}
		c.tools[record.ID] = call
	}
	return nil
}
