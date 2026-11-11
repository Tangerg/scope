package interaction

import (
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Continuation state remains private to the dispatcher; only the committed
// question and its answer contract cross the input boundary.
type PendingToolInput struct {
	processID run.ProcessID
	wait      run.Wait
}

func (p PendingToolInput) ProcessID() run.ProcessID          { return p.processID }
func (p PendingToolInput) WaitKey() run.WaitKey              { return p.wait.Key }
func (p PendingToolInput) Prompt() run.Payload               { return p.wait.Question }
func (p PendingToolInput) ResponseSchema() jsonschema.Schema { return p.wait.AnswerSchema }

func (p PendingToolInput) Valid() bool {
	return p.processID.Valid() && p.wait.Valid() && p.wait.Question.Valid() && p.wait.AnswerSchema.Valid()
}

// ResponseSignal addresses response to this question after checking it with
// the committed Wait's own admission rule.
func (p PendingToolInput) ResponseSignal(id run.SignalID, response run.Payload) (run.Signal, error) {
	if !p.Valid() || !id.Valid() {
		return run.Signal{}, ErrInvalidPendingToolInput
	}
	if err := p.wait.Admits(response); err != nil {
		return run.Signal{}, err
	}
	return run.Signal{ID: id, Wait: p.wait.Key, Payload: response}, nil
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
