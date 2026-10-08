package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
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

// Reference uses the original model response index, not an entry's position in
// this possibly sparse projection. A missing result does not invalidate a call's
// reference; false means that index is outside the admitted round.
func (r RoundResults) Reference(toolCallIndex uint32) (ToolCallRef, bool) {
	if !r.relation.Valid() || r.sequence == 0 || toolCallIndex >= r.callCount {
		return ToolCallRef{}, false
	}
	return ToolCallRef{processID: r.relation.ProcessID(), modelCallSequence: r.sequence, toolCallIndex: toolCallIndex}, true
}

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

const invalidEnumName = "invalid"

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
// This is a read-only projection of current recovery facts, not a transcript:
// later cuts may retire an admitted round, and overlapping cuts repeat facts
// that RoundResults.Reference identifies. A Host that keeps result history
// derives it inside its TreeCommitter transactions. Reading or storing this
// projection never authorizes tool reexecution.
func SettledResults(snapshot agent.TreeSnapshot) ([]RoundResults, error) {
	if !snapshot.Valid() {
		return nil, fmt.Errorf("%w: invalid tree", ErrInvalidResult)
	}
	processes := snapshot.ProcessSnapshots()
	children := newChildIndex(processes)
	var rounds []RoundResults
	for index := len(processes) - 1; index >= 0; index-- {
		round, err := children.settledProcessResults(snapshot, processes[index])
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

func pendingRound(process agent.ProcessSnapshot) (*executionState, []chat.ToolCall, error) {
	if process.CommittedExecutionState().Kind() != executionStateKind {
		return nil, nil, nil
	}
	state, err := process.CommittedExecutionState().Decode[executionState](executionStateKind)
	if err != nil {
		return nil, nil, err
	}
	if state.Completed {
		return nil, nil, nil
	}
	if envelopeErr := state.validateEnvelope(); envelopeErr != nil {
		return nil, nil, envelopeErr
	}
	if state.ToolRound == nil {
		return nil, nil, nil
	}
	calls, err := validatedToolCalls(state.ToolRound.Response)
	if err != nil {
		return nil, nil, err
	}
	if state.ModelCallCount == 0 || len(calls) == 0 || uint64(len(calls)) > math.MaxUint32 {
		return nil, nil, ErrInvalidExecutionState
	}
	if validationErr := state.ToolRound.validateResults(context.Background(), calls); validationErr != nil {
		return nil, nil, validationErr
	}
	return &state, calls, nil
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

type childIndex map[agent.ProcessID]map[agent.ChildKey]agent.ProcessSnapshot

func newChildIndex(processes []agent.ProcessSnapshot) childIndex {
	children := make(childIndex)
	for _, process := range processes {
		if parent, child := process.Relation().ParentID(); child {
			key, _ := process.Relation().ChildKey()
			if children[parent] == nil {
				children[parent] = make(map[agent.ChildKey]agent.ProcessSnapshot)
			}
			children[parent][key] = process
		}
	}
	return children
}

func (c childIndex) settledProcessResults(snapshot agent.TreeSnapshot, process agent.ProcessSnapshot) (RoundResults, error) {
	state, calls, err := pendingRound(process)
	if err != nil || state == nil {
		return RoundResults{}, err
	}
	refusals, err := delegateStartRefusals(snapshot, process, state)
	if err != nil {
		return RoundResults{}, err
	}
	round := RoundResults{relation: process.Relation(), sequence: state.ModelCallCount, callCount: uint32(len(calls))}
	for index, call := range calls {
		result := state.ToolRound.knownResult(index)
		if result == nil {
			result, err = c.settledChildResult(snapshot, process, refusals, state.ModelCallCount, uint32(index), call)
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
		round.entries = append(round.entries, ResultEntry{ToolCallIndex: uint32(index), Call: call, Result: result.toolResult(call), Disposition: result.Disposition})
	}
	if len(round.entries) == 0 {
		return RoundResults{}, nil
	}
	if err := round.seal(); err != nil {
		return RoundResults{}, err
	}
	return round, nil
}

func (c childIndex) settledChildResult(snapshot agent.TreeSnapshot, process agent.ProcessSnapshot, refusals map[agent.ChildKey]agent.Failure, sequence uint64, index uint32, call chat.ToolCall) (*toolCallResult, error) {
	toolKey, err := ToolChildKey(sequence, index)
	if err != nil {
		return nil, err
	}
	if child, found := c[process.ProcessID()][toolKey]; found {
		return settledToolResult(snapshot, child)
	}
	delegateKey, err := DelegateChildKey(sequence, call)
	if err != nil {
		return nil, err
	}
	child, found := c[process.ProcessID()][delegateKey]
	if !found {
		if failure, refused := refusals[delegateKey]; refused {
			return rejectedDelegateStartResult(call, failure), nil
		}
		return nil, nil
	}
	if !c.subtreeSettled(child) {
		return nil, nil
	}
	outcome, _ := child.Result()
	converted, err := delegateToolResult(call, outcome)
	if err != nil {
		return nil, err
	}
	return new(newToolCallResult(converted)), nil
}

func (c childIndex) subtreeSettled(process agent.ProcessSnapshot) bool {
	if !process.Status().Terminal() || len(process.UnknownEffectIDs()) != 0 {
		return false
	}
	for _, child := range c[process.ProcessID()] {
		if !c.subtreeSettled(child) {
			return false
		}
	}
	return true
}

// settledToolResult reads the child the call's ChildKey names. The Engine
// started it from the parent's own request, so its input is not re-checked
// against the parent's response.
func settledToolResult(snapshot agent.TreeSnapshot, process agent.ProcessSnapshot) (*toolCallResult, error) {
	// A completed Tool child's Output is its result; its state never repeats it.
	if result, terminal := process.Result(); terminal && result.Termination().Status() == agent.StatusCompleted {
		output, _ := result.Termination().Output()
		completion, err := output.Decode[toolCallResult]()
		if err != nil {
			return nil, err
		}
		return &completion, nil
	}
	for _, payload := range definiteDispatcherPayloads(snapshot, process) {
		envelope, err := decodeSignal(payload)
		if err != nil {
			return nil, err
		}
		if envelope.operation() == operationToolCall && envelope.ToolResult.Completion != nil {
			return envelope.ToolResult.Completion, nil
		}
	}
	return nil, nil
}

// definiteDispatcherPayloads returns the successful dispatcher outcomes a Tool
// child retains: its prepared dispatcher settlements and the settlement
// Signals it has not consumed. Wait openings belong to the Engine.
func definiteDispatcherPayloads(snapshot agent.TreeSnapshot, process agent.ProcessSnapshot) []json.RawMessage {
	var payloads []json.RawMessage
	for effectID, settlement := range process.Settlements() {
		request, found := snapshot.EffectRequest(process.ProcessID(), effectID)
		if found && request.Effect().Target() == agent.EffectTargetDispatcher && settlement.Status() == agent.SettlementStatusSucceeded {
			payloads = append(payloads, settlement.Payload())
		}
	}
	for _, receipt := range process.SignalReceipts() {
		signal, pending := receipt.PendingSignal()
		if !pending {
			continue
		}
		if settlement, err := agent.ParseSettlement(signal); err == nil && settlement.Status() == agent.SettlementStatusSucceeded {
			payloads = append(payloads, settlement.Payload())
		}
	}
	return payloads
}

// delegateStartRefusals reads each definite child-start refusal against the
// request that owns its key: a prepared settlement names its Effect, and a
// pending settlement answers the next unstarted invocation of the committed
// Delegate batch, because starts settle in declaration order.
func delegateStartRefusals(snapshot agent.TreeSnapshot, process agent.ProcessSnapshot, state *executionState) (map[agent.ChildKey]agent.Failure, error) {
	refusals := make(map[agent.ChildKey]agent.Failure)
	for effectID, settlement := range process.Settlements() {
		request, found := snapshot.EffectRequest(process.ProcessID(), effectID)
		if !found || settlement.Status() != agent.SettlementStatusFailed {
			continue
		}
		spec, err := agent.ParseChildStartEffect(request.Effect())
		if err != nil {
			continue
		}
		var start agent.ChildStartResult
		if err := jsonv2.Unmarshal(settlement.Payload(), &start); err != nil {
			return nil, err
		}
		if failure, failed := start.Failure(); failed {
			refusals[spec.Key] = failure
		}
	}
	batch := state.ToolRound.ChildBatch
	if batch == nil || batch.Kind != childCallsDelegate {
		return refusals, nil
	}
	calls, err := state.ToolRound.activeCalls(context.Background())
	if err != nil {
		return nil, err
	}
	keys, err := batch.childKeys(state.ModelCallCount, state.ToolRound.nextCallIndex(), calls)
	if err != nil {
		return nil, err
	}
	var unstarted []agent.ChildKey
	for index, invocation := range batch.Invocations {
		if invocation != nil && invocation.ProcessID == nil && invocation.Result == nil {
			unstarted = append(unstarted, keys[index])
		}
	}
	for _, receipt := range process.SignalReceipts() {
		signal, pending := receipt.PendingSignal()
		if !pending {
			continue
		}
		// The parent also retains model, steer and child-wait Signals. Only the
		// Framework's strict child-start decoder accepts a start settlement.
		start, err := agent.ParseChildStartResult(signal)
		if err != nil {
			continue
		}
		if len(unstarted) == 0 {
			return nil, fmt.Errorf("%w: child start answers no pending Delegate", ErrInvalidExecutionState)
		}
		if failure, failed := start.Failure(); failed {
			refusals[unstarted[0]] = failure
		}
		unstarted = unstarted[1:]
	}
	return refusals, nil
}
