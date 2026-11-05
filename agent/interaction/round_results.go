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

// callIndex finds a committed tool intent's call in the proposal that produced
// it; the model owns call IDs, so an ID naming different content is forged.
func (r *RoundResults) callIndex(call chat.ToolCall) (uint32, bool) {
	if r == nil {
		return 0, false
	}
	for index, candidate := range r.calls {
		if candidate.ID == call.ID {
			return uint32(index), candidate == call
		}
	}
	return 0, false
}

// delegateIndex finds the proposed call a Spawn intent executes. The child key
// is derived from the call, so exactly one declared delegate call can match.
func (r *RoundResults) delegateIndex(definition *definition, spec run.ChildSpec) (uint32, error) {
	for index, call := range r.calls {
		delegate, declared := definition.delegate(call.Name)
		if !declared {
			continue
		}
		key, err := DelegateChildKey(r.sequence, call)
		if err != nil {
			return 0, err
		}
		if key != spec.Key {
			continue
		}
		if err := delegate.validateChild(spec, r.sequence, call); err != nil {
			return 0, fmt.Errorf("%w: delegate intent: %w", ErrInvalidResult, err)
		}
		return uint32(index), nil
	}
	return 0, ErrInvalidResult
}

// claim binds a proposed call to the one Effect that executes it.
func (r *RoundResults) claim(index uint32, effect run.EffectID) error {
	if _, claimed := r.references[index]; claimed {
		return ErrInvalidResult
	}
	r.references[index] = effect
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
