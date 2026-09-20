package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// RoundResults is an immutable, possibly sparse view of definite results for
// one Interaction round. Entries retain original call indexes. Complete reports
// knowledge, never permission to continue a canceled Process; only the Interaction
// state machine admits a complete round through a Checkpoint before continuation.
type RoundResults struct {
	relation  agent.ProcessRelation
	sequence  uint64
	callCount uint32
	entries   []ResultEntry
	data      json.RawMessage
	digest    agent.Digest
}

func (r RoundResults) Relation() agent.ProcessRelation { return r.relation }
func (r RoundResults) ModelCallSequence() uint64       { return r.sequence }
func (r RoundResults) CallCount() uint32               { return r.callCount }
func (r RoundResults) Complete() bool {
	return uint64(len(r.entries)) == uint64(r.callCount) && r.callCount > 0
}

// Digest binds the exact JSON content and attribution, independently of EffectIDs
// and tree incarnation. It is a content hash, not a storage acknowledgment.
func (r RoundResults) Digest() agent.Digest  { return r.digest }
func (r RoundResults) JSON() json.RawMessage { return bytes.Clone(r.data) }
func (r RoundResults) Entries() []ResultEntry {
	entries := slices.Clone(r.entries)
	for index := range entries {
		entries[index].Result = entries[index].Result.Clone()
	}
	return entries
}

// ResultDisposition describes a definite outcome. Unknown execution outcomes
// and input checkpoints never enter RoundResults. Rejection does not assert
// that a Tool's business operation ran.
type ResultDisposition string

const (
	ResultInvalid   ResultDisposition = ""
	ResultSucceeded ResultDisposition = "succeeded"
	ResultFailed    ResultDisposition = "failed"
	ResultRejected  ResultDisposition = "rejected"
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

// ResultEntry keeps exact provider attribution and output together. Entries
// returned by RoundResults are independent copies and cannot alter the snapshot.
type ResultEntry struct {
	ToolCallIndex uint32            `json:"tool_call_index"`
	Call          chat.ToolCall     `json:"call"`
	Result        chat.ToolResult   `json:"result"`
	Disposition   ResultDisposition `json:"disposition"`
}

// SettledResults interprets Scope-owned facts in one complete tree cut, ordered
// child-before-parent and then by original call index. Unknown operations,
// waiting checkpoints and calls that never started produce no synthetic result.
// Definite tool settlements remain visible even when cancellation prevented the
// Tool or parent Step from consuming them. Delegate projection requires a fully
// terminal subtree without unresolved Effects.
//
// This is a read-only projection of current recovery facts, not a historical
// transcript or a required committer adapter. Later cuts may retire an admitted
// round. TreeCommitter preserves recovery facts whether or not this view is read.
//
// Hosts that need a separate result history can derive it within each
// TreeCommitter transaction, atomically with the proposed head and writer fence.
// Overlapping cuts repeat facts: (ProcessID, ModelCallSequence, ToolCallIndex)
// identifies one logical call. The host owns history storage, retention, and
// delivery; reading or storing this projection never authorizes tool reexecution.
func SettledResults(snapshot agent.TreeSnapshot) ([]RoundResults, error) {
	if !snapshot.Valid() {
		return nil, fmt.Errorf("%w: invalid tree", ErrInvalidResult)
	}
	processes := snapshot.ProcessSnapshots()
	children := make(map[agent.ProcessID]map[agent.ChildKey]agent.ProcessSnapshot)
	for _, process := range processes {
		if parent, child := process.Relation().ParentID(); child {
			key, _ := process.Relation().ChildKey()
			if children[parent] == nil {
				children[parent] = make(map[agent.ChildKey]agent.ProcessSnapshot)
			}
			children[parent][key] = process
		}
	}
	var rounds []RoundResults
	for index := len(processes) - 1; index >= 0; index-- {
		process := processes[index]
		round, err := settledProcessResults(process, children)
		if err != nil {
			return nil, err
		}
		if len(round.entries) == 0 {
			continue
		}
		rounds = append(rounds, round)
	}
	return rounds, nil
}

func settledProcessResults(process agent.ProcessSnapshot, children map[agent.ProcessID]map[agent.ChildKey]agent.ProcessSnapshot) (RoundResults, error) {
	if process.CommittedExecutionState().Kind() != executionStateKind {
		return RoundResults{}, nil
	}
	state, err := process.CommittedExecutionState().Decode[executionState](executionStateKind)
	if err != nil {
		return RoundResults{}, err
	}
	if envelopeErr := state.validateEnvelope(); envelopeErr != nil {
		return RoundResults{}, envelopeErr
	}
	if state.ToolRound == nil {
		return RoundResults{}, nil
	}
	calls, err := validatedToolCalls(state.ToolRound.Response)
	if err != nil {
		return RoundResults{}, err
	}
	if state.ModelCallCount == 0 || len(calls) == 0 || uint64(len(calls)) > uint64(^uint32(0)) {
		return RoundResults{}, ErrInvalidExecutionState
	}
	if validationErr := state.ToolRound.validateResults(context.Background(), calls); validationErr != nil {
		return RoundResults{}, validationErr
	}
	round := RoundResults{relation: process.Relation(), sequence: state.ModelCallCount, callCount: uint32(len(calls))}
	for index, call := range calls {
		result := state.ToolRound.knownResult(index)
		if result == nil {
			result, err = settledChildResult(process, children, state.ModelCallCount, uint32(index), call)
			if err != nil {
				return RoundResults{}, err
			}
		}
		if result == nil {
			continue
		}
		if validationErr := result.validateCall(call); validationErr != nil {
			return RoundResults{}, validationErr
		}
		disposition := ResultSucceeded
		if result.Rejected {
			disposition = ResultRejected
		} else if result.Result.IsError {
			disposition = ResultFailed
		}
		round.entries = append(round.entries, ResultEntry{ToolCallIndex: uint32(index), Call: call, Result: result.Result.Clone(), Disposition: disposition})
	}
	if len(round.entries) == 0 {
		return RoundResults{}, nil
	}

	if err := round.seal(); err != nil {
		return RoundResults{}, err
	}
	return round, nil
}

func (r *RoundResults) seal() error {
	wire := roundResultsWire{ProcessID: r.relation.ProcessID(), RootID: r.relation.RootID(), ModelCallSequence: r.sequence, CallCount: r.callCount, Entries: r.entries}
	if parent, child := r.relation.ParentID(); child {
		key, _ := r.relation.ChildKey()
		wire.ParentID, wire.ChildKey = &parent, &key
	}
	data, err := jsonv2.Marshal(wire, jsonv2.Deterministic(true))
	if err != nil {
		return err
	}
	r.data, r.digest = data, agent.ComputeDigest(data)

	return nil
}

type roundResultsWire struct {
	ProcessID         agent.ProcessID  `json:"process_id"`
	RootID            agent.ProcessID  `json:"root_id"`
	ParentID          *agent.ProcessID `json:"parent_id,omitzero"`
	ChildKey          *agent.ChildKey  `json:"child_key,omitzero"`
	ModelCallSequence uint64           `json:"model_call_sequence"`
	CallCount         uint32           `json:"call_count"`
	Entries           []ResultEntry    `json:"entries"`
}

func settledChildResult(process agent.ProcessSnapshot, children map[agent.ProcessID]map[agent.ChildKey]agent.ProcessSnapshot, sequence uint64, index uint32, call chat.ToolCall) (*toolCallResult, error) {
	toolKey, err := toolChildKey(sequence, call)
	if err != nil {
		return nil, err
	}
	if child, found := children[process.ProcessID()][toolKey]; found {
		return settledToolResult(child, sequence, index, call)
	}
	delegateKey, err := DelegateChildKey(sequence, call)
	if err != nil {
		return nil, err
	}
	child, found := children[process.ProcessID()][delegateKey]
	if !found {
		return rejectedDelegateStart(process, delegateKey, call), nil
	}
	if !subtreeSettled(child, children) {
		return nil, nil
	}
	outcome, _ := child.Result()
	converted, err := delegateToolResult(call, outcome)
	if err != nil {
		return nil, err
	}
	return &toolCallResult{Result: converted}, nil
}

func settledToolResult(process agent.ProcessSnapshot, sequence uint64, index uint32, call chat.ToolCall) (*toolCallResult, error) {
	state, err := decodeToolState(process.CommittedExecutionState())
	if err != nil {
		return nil, err
	}
	if state.Call.ModelCallSequence != sequence || state.Call.ToolCallIndex != index || state.Call.Call != call {
		return nil, ErrInvalidExecutionState
	}
	if state.Result != nil {
		return state.Result, nil
	}
	for _, payload := range definitePayloads(process) {
		envelope, err := decodeSignal(payload)
		if err != nil {
			return nil, err
		}
		if envelope.Operation == operationToolCall && envelope.ToolResult.Completion != nil {
			return envelope.ToolResult.Completion, nil
		}
	}
	return nil, nil
}

func definitePayloads(process agent.ProcessSnapshot) []json.RawMessage {
	var payloads []json.RawMessage
	for _, settlement := range process.Settlements() {
		if settlement.Status() != agent.SettlementStatusUnknown {
			payloads = append(payloads, settlement.Payload())
		}
	}
	for _, receipt := range process.SignalReceipts() {
		if signal, pending := receipt.PendingSignal(); pending && signal.EngineOwned() {
			payloads = append(payloads, signal.Payload())
		}
	}
	return payloads
}

func rejectedDelegateStart(process agent.ProcessSnapshot, key agent.ChildKey, call chat.ToolCall) *toolCallResult {
	for _, payload := range definitePayloads(process) {
		var start agent.ChildStartResult
		// The parent also retains model, steer and child-wait Signals. Only the
		// Framework's strict child-start decoder can supply a start refusal.
		if err := json.Unmarshal(payload, &start); err != nil {
			continue
		}
		if start.Key() != key {
			continue
		}
		if failure, failed := start.Failure(); failed {
			result := delegateErrorResult(call, "child start failed: "+failure.Code()+": "+failure.Message())
			return &toolCallResult{Result: result, Rejected: true}
		}
	}
	return nil
}

func subtreeSettled(process agent.ProcessSnapshot, children map[agent.ProcessID]map[agent.ChildKey]agent.ProcessSnapshot) bool {
	if !process.Status().Terminal() || len(process.UnknownEffectIDs()) != 0 {
		return false
	}
	for _, child := range children[process.ProcessID()] {
		if !subtreeSettled(child, children) {
			return false
		}
	}
	return true
}
