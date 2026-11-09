package interaction

import (
	"encoding/json"

	"github.com/Tangerg/scope/agent/run"
)

// Continuation state remains private to the dispatcher; only the committed
// question and its answer contract cross the input boundary.
type PendingToolInput struct {
	processID run.ProcessID
	wait      run.Wait
}

func (p PendingToolInput) ProcessID() run.ProcessID        { return p.processID }
func (p PendingToolInput) WaitKey() run.WaitKey            { return p.wait.Key }
func (p PendingToolInput) Prompt() json.RawMessage         { return p.wait.Question.JSON() }
func (p PendingToolInput) ResponseSchema() json.RawMessage { return p.wait.AnswerSchema.JSON() }
func (p PendingToolInput) Valid() bool {
	return p.processID.Valid() && p.wait.Valid() && p.wait.Question.Valid() && p.wait.AnswerSchema.Valid()
}

// ResponseSignal addresses response to this question after checking it with
// the committed Wait's own admission rule.
func (p PendingToolInput) ResponseSignal(id run.SignalID, response json.RawMessage) (run.Signal, error) {
	if !p.Valid() || !id.Valid() {
		return run.Signal{}, ErrInvalidPendingToolInput
	}
	payload, err := run.ParsePayload(response)
	if err != nil {
		return run.Signal{}, err
	}
	if err := p.wait.Admits(payload); err != nil {
		return run.Signal{}, err
	}
	return run.Signal{ID: id, Wait: p.wait.Key, Payload: payload}, nil
}

// PendingToolInputs lists the tool questions awaiting an answer. Every Wait an
// interaction opens is a tool question, so the committed waits are the one
// complete record of them and need no history.
func PendingToolInputs[I, O any](program run.Program[I, O], inspection run.TreeInspection) []PendingToolInput {
	var pending []PendingToolInput
	for _, process := range inspection.Processes {
		if process.Program != program.ProgramRef() {
			continue
		}
		for _, wait := range process.Waits {
			pending = append(pending, PendingToolInput{processID: process.ID, wait: wait})
		}
	}
	return pending
}
