package interaction

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

// Continuation state remains private to the dispatcher; only the question and
// answer contract cross the input boundary.
type PendingToolInput struct {
	processID      run.ProcessID
	waitKey        run.WaitKey
	prompt         json.RawMessage
	responseSchema jsonschema.Schema
}

func (p PendingToolInput) ProcessID() run.ProcessID        { return p.processID }
func (p PendingToolInput) WaitKey() run.WaitKey            { return p.waitKey }
func (p PendingToolInput) Prompt() json.RawMessage         { return bytes.Clone(p.prompt) }
func (p PendingToolInput) ResponseSchema() json.RawMessage { return p.responseSchema.JSON() }
func (p PendingToolInput) Valid() bool {
	return p.processID.Valid() && p.waitKey.Valid() && len(p.prompt) > 0 && p.responseSchema.Valid()
}
func (p PendingToolInput) ResponseSignal(id run.SignalID, response json.RawMessage) (run.Signal, error) {
	if !p.Valid() || !id.Valid() {
		return run.Signal{}, ErrInvalidPendingToolInput
	}
	if len(response) == 0 || len(response) > tool.MaxInputBytes {
		return run.Signal{}, fmt.Errorf("%w: response exceeds input bound", tool.ErrInvalidInputRequest)
	}
	payload, err := run.ParsePayload(response)
	if err != nil {
		return run.Signal{}, err
	}
	if len(payload.JSON()) > tool.MaxInputBytes {
		return run.Signal{}, fmt.Errorf("%w: normalized response exceeds input bound", tool.ErrInvalidInputRequest)
	}
	if err := p.responseSchema.Validate(payload.JSON()); err != nil {
		return run.Signal{}, err
	}
	return run.Signal{ID: id, Wait: p.waitKey, Payload: payload}, nil
}

// PendingToolInputs lists the tool questions awaiting an answer. Every Wait an
// interaction opens is a tool question, so the committed waits are the one
// complete record of them and need no history.
func PendingToolInputs(program run.Program[Input, Output], inspection run.TreeInspection) []PendingToolInput {
	var pending []PendingToolInput
	for _, process := range inspection.Processes {
		if process.Program != program.ProgramRef() {
			continue
		}
		for _, wait := range process.Waits {
			pending = append(pending, PendingToolInput{processID: process.ID, waitKey: wait.Key, prompt: wait.Question.JSON(), responseSchema: wait.AnswerSchema})
		}
	}
	return pending
}
