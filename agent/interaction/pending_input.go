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
func PendingToolInputs(program run.Program[Input, Output], log run.Log) ([]PendingToolInput, error) {
	projection, err := projectInteractions(program, log)
	if err != nil {
		return nil, err
	}
	inspection, err := log.Inspect()
	if err != nil {
		return nil, err
	}
	var pending []PendingToolInput
	for _, process := range inspection.Processes {
		if process.Program != program.ProgramRef() {
			continue
		}
		for _, wait := range process.Waits {
			matched := false
			for effect, input := range projection.inputs {
				expected, waitErr := input.wait()
				if waitErr != nil {
					return nil, waitErr
				}
				if effect.Process != process.ID || expected.Key != wait.Key {
					continue
				}
				if !bytes.Equal(wait.Question.JSON(), expected.Question.JSON()) || !wait.AnswerSchema.Equal(expected.AnswerSchema) || wait.MaxAnswerBytes != expected.MaxAnswerBytes {
					return nil, fmt.Errorf("%w: wait differs from its committed tool question", ErrInvalidResult)
				}
				pending = append(pending, PendingToolInput{processID: process.ID, waitKey: wait.Key, prompt: wait.Question.JSON(), responseSchema: wait.AnswerSchema})
				matched = true
				break
			}
			if !matched {
				return nil, fmt.Errorf("%w: wait has no committed tool question", ErrInvalidResult)
			}
		}
	}
	return pending, nil
}
