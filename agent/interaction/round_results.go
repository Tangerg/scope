package interaction

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

type ResultDisposition string

const (
	ResultInvalid   ResultDisposition = ""
	ResultSucceeded ResultDisposition = "succeeded"
	ResultFailed    ResultDisposition = "failed"
	ResultRejected  ResultDisposition = "rejected"
	invalidEnumName                   = "invalid"
)

func (r ResultDisposition) Valid() bool {
	return r == ResultSucceeded || r == ResultFailed || r == ResultRejected
}
func (r ResultDisposition) String() string {
	if !r.Valid() {
		return invalidEnumName
	}
	return string(r)
}

// A sparse round preserves the provider's indexes. Missing outcomes never
// authorize retry, and an input request does not establish a final result.
type RoundResults struct {
	model      run.EffectID
	sequence   uint64
	calls      []chat.ToolCall
	references map[uint32]run.EffectID
	entries    []ResultEntry
	data       json.RawMessage
}

func (r RoundResults) ProcessID() run.ProcessID    { return r.model.Process }
func (r RoundResults) ModelEffectID() run.EffectID { return r.model }
func (r RoundResults) ModelCallSequence() uint64   { return r.sequence }
func (r RoundResults) CallCount() uint32           { return uint32(len(r.calls)) }
func (r RoundResults) Reference(index uint32) (run.EffectID, bool) {
	reference, found := r.references[index]
	return reference, found
}
func (r RoundResults) Complete() bool        { return len(r.calls) > 0 && len(r.entries) == len(r.calls) }
func (r RoundResults) JSON() json.RawMessage { return bytes.Clone(r.data) }
func (r RoundResults) Digest() run.Digest    { return run.ComputeDigest(r.data) }
func (r RoundResults) Entries() []ResultEntry {
	entries := slices.Clone(r.entries)
	for index := range entries {
		entries[index].Result = entries[index].Result.Clone()
	}
	return entries
}
func (r *RoundResults) seal() error {
	slices.SortFunc(r.entries, func(left, right ResultEntry) int { return int(left.ToolCallIndex) - int(right.ToolCallIndex) })
	data, err := jsonv2.Marshal(roundResultsWire{Model: r.model, Sequence: r.sequence, CallCount: uint32(len(r.calls)), Entries: r.entries}, jsonv2.Deterministic(true))
	if err != nil {
		return err
	}
	r.data = data
	return nil
}
func (r *RoundResults) result(index uint32, result toolCallResult) error {
	if int(index) >= len(r.calls) {
		return ErrInvalidResult
	}
	call := r.calls[index]
	if err := result.validateCall(call); err != nil {
		return err
	}
	entry := ResultEntry{Reference: r.references[index], ToolCallIndex: index, Call: call, Result: result.toolResult(call), Disposition: result.Disposition}
	for position := range r.entries {
		if r.entries[position].ToolCallIndex == index {
			r.entries[position] = entry
			return nil
		}
	}
	r.entries = append(r.entries, entry)
	return nil
}

type ResultEntry struct {
	Reference     run.EffectID      `json:"reference"`
	ToolCallIndex uint32            `json:"tool_call_index"`
	Call          chat.ToolCall     `json:"call"`
	Result        chat.ToolResult   `json:"result"`
	Disposition   ResultDisposition `json:"disposition"`
}
type roundResultsWire struct {
	Model     run.EffectID  `json:"model"`
	Sequence  uint64        `json:"model_call_sequence"`
	CallCount uint32        `json:"call_count"`
	Entries   []ResultEntry `json:"entries"`
}

// SettledResults projects the complete history that run.Runtime.History reads.
// Only committed intents establish references. Locally rejected proposals that
// never emitted an effect are not presented as externally executed operations.
func SettledResults(program run.Program[Input, Output], log run.Log) ([]RoundResults, error) {
	projection, err := projectInteractions(program, log)
	if err != nil {
		return nil, err
	}
	var result []RoundResults
	for _, round := range projection.rounds {
		if len(round.entries) == 0 {
			continue
		}
		if err := round.seal(); err != nil {
			return nil, err
		}
		result = append(result, *round)
	}
	return result, nil
}

type projectedCall struct {
	round *RoundResults
	index uint32
	input *inputWait
}

func (p *projectedCall) resume(request toolDispatchRequest, effect run.EffectID) error {
	resume := request.Resume
	if resume == nil || !resume.precedes(effect) || p.input == nil || p.round.references[p.index] != resume.Reference || p.round.calls[p.index] != request.Invocation.Call {
		return ErrInvalidResult
	}
	question := p.input.Request
	if !question.ResponseSchema().Equal(resume.InputRequest.ResponseSchema()) || !bytes.Equal(question.Prompt(), resume.InputRequest.Prompt()) || !bytes.Equal(question.State(), resume.InputRequest.State()) {
		return ErrInvalidResult
	}
	p.input = nil
	return nil
}

type interactionProjection struct {
	rounds    []*RoundResults
	delegates []DelegateAttribution
}

func projectInteractions(program run.Program[Input, Output], log run.Log) (interactionProjection, error) {
	if !program.Valid() {
		return interactionProjection{}, ErrInvalidResult
	}
	definition, bound := program.Definition().(*definition)
	if !bound {
		return interactionProjection{}, ErrInvalidResult
	}
	if !log.Complete() {
		return interactionProjection{}, fmt.Errorf("%w: projection requires the complete history", ErrInvalidResult)
	}
	if err := log.Validate(); err != nil {
		return interactionProjection{}, err
	}
	projection := interactionProjection{}
	processes := make(map[run.ProcessID]bool)
	intents := make(map[run.EffectID]run.EffectIntent)
	models := make(map[run.EffectID]modelCall)
	active := make(map[run.ProcessID]*RoundResults)
	calls := make(map[run.EffectID]*projectedCall)
	delegates := make(map[run.ProcessID]*projectedCall)
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Started:
			processes[record.Process] = record.Program == program.ProgramRef()
		case run.EffectIntent:
			if !processes[record.ID.Process] {
				continue
			}
			intents[record.ID] = record
			if record.Effect.Target() == run.EffectTargetDispatcher {
				envelope, err := decodeEffect(record.Effect.Payload())
				if err != nil {
					return interactionProjection{}, err
				}
				switch envelope.operation() {
				case operationModelCall:
					models[record.ID] = *envelope.ModelCall
				case operationToolCall:
					call := envelope.ToolCall
					if call.Resume != nil {
						previous, found := calls[call.Resume.Reference]
						if !found {
							return interactionProjection{}, ErrInvalidResult
						}
						if err := previous.resume(*call, record.ID); err != nil {
							return interactionProjection{}, err
						}
						calls[record.ID] = previous
						continue
					}
					round := active[record.ID.Process]
					index, found := roundCall(round, call.Invocation.Call)
					if !found {
						return interactionProjection{}, ErrInvalidResult
					}
					if _, duplicate := round.references[index]; duplicate {
						return interactionProjection{}, ErrInvalidResult
					}
					round.references[index] = record.ID
					calls[record.ID] = &projectedCall{round: round, index: index}
				}
			} else if record.Effect.Target() == run.EffectTargetFramework {
				spec, err := run.ParseChildStartEffect(record.Effect)
				if err != nil {
					return interactionProjection{}, err
				}
				round := active[record.ID.Process]
				if round == nil {
					return interactionProjection{}, ErrInvalidResult
				}
				matched := false
				for index, call := range round.calls {
					delegate, declared := definition.delegate(call.Name)
					if !declared {
						continue
					}
					key, err := DelegateChildKey(round.sequence, call)
					if err != nil {
						return interactionProjection{}, err
					}
					if key != spec.Key {
						continue
					}
					if bindingErr := delegate.validateChild(spec, round.sequence, call); bindingErr != nil {
						return interactionProjection{}, fmt.Errorf("%w: delegate intent: %w", ErrInvalidResult, bindingErr)
					}
					if _, duplicate := round.references[uint32(index)]; duplicate {
						return interactionProjection{}, ErrInvalidResult
					}
					round.references[uint32(index)] = record.ID
					calls[record.ID] = &projectedCall{round: round, index: uint32(index)}
					matched = true
					break
				}
				if !matched {
					return interactionProjection{}, ErrInvalidResult
				}
			}
		case run.EffectSettled:
			if !processes[record.ID.Process] || record.Failure != nil {
				continue
			}
			intent, found := intents[record.ID]
			if !found {
				return interactionProjection{}, ErrInvalidResult
			}
			if model, modelFound := models[record.ID]; modelFound {
				if record.Outcome.Status() != run.SettlementStatusSucceeded {
					continue
				}
				envelope, err := decodeResult(record.Outcome.Payload())
				if err != nil {
					return interactionProjection{}, err
				}
				if envelope.ModelResult == nil {
					return interactionProjection{}, ErrInvalidResult
				}
				proposed, err := validatedToolCalls(envelope.ModelResult.Response)
				if err != nil {
					return interactionProjection{}, err
				}
				round := &RoundResults{model: record.ID, sequence: model.ModelCallSequence, calls: proposed, references: make(map[uint32]run.EffectID)}
				active[record.ID.Process] = round
				projection.rounds = append(projection.rounds, round)
				continue
			}
			call, found := calls[record.ID]
			if !found {
				continue
			}
			if intent.Effect.Target() == run.EffectTargetFramework {
				if record.Outcome.Status() == run.SettlementStatusFailed {
					payload, err := run.ParsePayload(record.Outcome.Payload())
					if err != nil {
						return interactionProjection{}, err
					}
					failure, err := payload.Decode[run.Failure]()
					if err != nil {
						return interactionProjection{}, err
					}
					if err := call.round.result(call.index, *rejectedDelegateStartResult(call.round.calls[call.index], failure)); err != nil {
						return interactionProjection{}, err
					}
				} else if record.Outcome.Status() == run.SettlementStatusSucceeded {
					payload, err := run.ParsePayload(record.Outcome.Payload())
					if err != nil {
						return interactionProjection{}, err
					}
					child, err := payload.Decode[run.ProcessID]()
					if err != nil {
						return interactionProjection{}, err
					}
					delegates[child] = call
					projection.delegates = append(projection.delegates, DelegateAttribution{reference: record.ID, modelCallSequence: call.round.sequence, toolCallIndex: call.index, toolCall: call.round.calls[call.index], processID: child})
				}
				continue
			}
			if record.Outcome.Status() != run.SettlementStatusSucceeded {
				continue
			}
			envelope, err := decodeResult(record.Outcome.Payload())
			if err != nil {
				return interactionProjection{}, err
			}
			if envelope.ToolResult == nil {
				return interactionProjection{}, ErrInvalidResult
			}
			if request := envelope.ToolResult.InputRequest; request != nil {
				call.input = &inputWait{Effect: record.ID, Request: *request}
				continue
			}
			if err := call.round.result(call.index, *envelope.ToolResult.Completion); err != nil {
				return interactionProjection{}, err
			}
		case run.Exited:
			call, found := delegates[record.Process]
			if !found {
				continue
			}
			if output, completed := record.Termination.Output(); completed {
				delegate, _ := definition.delegate(call.round.calls[call.index].Name)
				if err := delegate.validateOutput(output); err != nil {
					return interactionProjection{}, fmt.Errorf("%w: delegated output: %w", ErrInvalidResult, err)
				}
			}
			result, err := delegateToolResult(call.round.calls[call.index], record.Termination)
			if err != nil {
				return interactionProjection{}, err
			}
			if err := call.round.result(call.index, newToolCallResult(result)); err != nil {
				return interactionProjection{}, err
			}
		}
	}
	return projection, nil
}
func roundCall(round *RoundResults, call chat.ToolCall) (uint32, bool) {
	if round == nil {
		return 0, false
	}
	for index, candidate := range round.calls {
		if candidate.ID == call.ID {
			if candidate != call {
				return 0, false
			}
			return uint32(index), true
		}
	}
	return 0, false
}
