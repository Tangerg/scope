package agent_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

var (
	errSuccessorConflict       = errors.New("successor request conflicts with its predecessor admission")
	errSuccessorAttemptExpired = errors.New("successor initialization attempt was fenced")
	errStartAcknowledgmentLost = errors.New("successor start acknowledgment lost")
	errUnsafeEpisodeBoundary   = errors.New("episode has no safe continuation boundary")
)

type successorRecord struct {
	request     successorRequest
	digest      agent.Digest
	fence       uint64
	prospective agent.ProcessID
	successor   agent.ProcessID
}

// The mutex models one database transaction over input cutover, successor
// admission, and tree creation. The underlying tree adapter retains its own
// canonical checkpoint and incarnation rules instead of reimplementing them.
type episodeStore struct {
	mu                      sync.Mutex
	trees                   *agenttest.MemoryTreeDurability
	sealed                  map[agent.ProcessID]agent.TreeSnapshot
	inputs                  map[agent.SignalID]episodeInput
	successors              map[agent.ProcessID]successorRecord
	allocations             uint32
	starts                  uint32
	loseStartAcknowledgment bool
}

func newEpisodeStore() *episodeStore {
	return &episodeStore{
		trees: agenttest.NewMemoryTreeDurability(), sealed: make(map[agent.ProcessID]agent.TreeSnapshot),
		inputs: make(map[agent.SignalID]episodeInput), successors: make(map[agent.ProcessID]successorRecord),
	}
}

func (e *episodeStore) claim(request successorRequest) (*episodeAttempt, agent.ProcessID, error) {
	digest, err := request.identity()
	if err != nil {
		return nil, agent.ProcessID{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.sealed[request.Predecessor].Valid() {
		return nil, agent.ProcessID{}, errUnsafeEpisodeBoundary
	}
	record, present := e.successors[request.Predecessor]
	if present && record.digest != digest {
		return nil, agent.ProcessID{}, errSuccessorConflict
	}
	if !present {
		record = successorRecord{request: request, digest: digest}
		e.allocations++
	}
	if !record.successor.Valid() {
		if record.fence == math.MaxUint64 {
			return nil, agent.ProcessID{}, errSuccessorAttemptExpired
		}
		record.fence++
		record.prospective = agent.ProcessID{}
	}
	e.successors[request.Predecessor] = record
	return &episodeAttempt{store: e, predecessor: request.Predecessor, fence: record.fence}, record.successor, nil
}

// Binding precedes submission. A sealed route rejects new bindings; an existing
// delivery retains its original Process and WaitID even after successor creation.
func (e *episodeStore) bindInput(process *agent.Process, request agent.SignalRequest) error {
	if !process.ID().Valid() || !request.Valid() {
		return agent.ErrInvalidSignalRequest
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if previous, present := e.inputs[request.ID()]; present {
		previousWait, _ := previous.request.WaitID()
		requestedWait, _ := request.WaitID()
		if previous.recipient != process.ID() || previousWait != requestedWait || agent.ComputeDigest(previous.request.Payload()) != agent.ComputeDigest(request.Payload()) {
			return agent.ErrSignalConflict
		}
		return nil
	}
	if e.sealed[process.Relation().RootID()].Valid() {
		return errUnsafeEpisodeBoundary
	}
	e.inputs[request.ID()] = episodeInput{root: process.Relation().RootID(), recipient: process.ID(), request: request, disposition: episodeInputPending}
	return nil
}

func (e *episodeStore) acknowledgeInput(recipient agent.ProcessID, id agent.SignalID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	record, present := e.inputs[id]
	if !present || record.recipient != recipient || record.disposition == episodeInputNotAdmitted {
		return agent.ErrSignalConflict
	}
	record.acknowledged = true
	e.inputs[id] = record
	return nil
}

// The initial continuation contract requires a successful root, all local work
// drained, and no unresolved external Effects anywhere in the tree. Retained
// inputs stay at the predecessor; this example does not transfer live work.
func (e *episodeStore) sealEpisode(ctx context.Context, process *agent.Process) (agent.Result, error) {
	if !process.Relation().IsRoot() {
		return agent.Result{}, errUnsafeEpisodeBoundary
	}
	result, err := process.Await(ctx)
	if err != nil {
		return agent.Result{}, err
	}
	if result.Status() != agent.StatusCompleted {
		return agent.Result{}, errUnsafeEpisodeBoundary
	}
	if joinErr := process.Join(ctx); joinErr != nil {
		return agent.Result{}, joinErr
	}
	head, present, err := e.trees.LoadTree(ctx, process.ID())
	if err != nil {
		return agent.Result{}, err
	}
	if !present || head.RootID() != process.ID() {
		return agent.Result{}, errUnsafeEpisodeBoundary
	}
	for _, snapshot := range head.ProcessSnapshots() {
		if !snapshot.Status().Terminal() || len(snapshot.UnknownEffectIDs()) != 0 {
			return agent.Result{}, errUnsafeEpisodeBoundary
		}
		if snapshot.ProcessID() == process.ID() && snapshot.Status() != agent.StatusCompleted {
			return agent.Result{}, errUnsafeEpisodeBoundary
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sealed[process.ID()].Valid() {
		return result, nil
	}
	resolved := make(map[agent.SignalID]episodeInput)
	for id, record := range e.inputs {
		if record.root != process.ID() {
			continue
		}
		record.disposition = episodeInputNotAdmitted
		for _, snapshot := range head.ProcessSnapshots() {
			if snapshot.ProcessID() != record.recipient {
				continue
			}
			for _, receipt := range snapshot.SignalReceipts() {
				if receipt.ID() != id {
					continue
				}
				if !receipt.Matches(record.request) {
					return agent.Result{}, agent.ErrSignalConflict
				}
				record.disposition = episodeInputRetained
				if receipt.Consumed() {
					record.disposition = episodeInputConsumed
				}
			}
		}
		if record.acknowledged && record.disposition == episodeInputNotAdmitted {
			return agent.Result{}, errors.New("acknowledged input absent from final authoritative tree")
		}
		resolved[id] = record
	}
	for id, record := range resolved {
		e.inputs[id] = record
	}
	e.sealed[process.ID()] = head
	return result, nil
}

type episodeAttempt struct {
	store       *episodeStore
	predecessor agent.ProcessID
	fence       uint64
}

func (e *episodeAttempt) Admit(_ context.Context, admission agent.ProcessAdmission) error {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	record := e.store.successors[e.predecessor]
	if !admission.Valid() || record.fence != e.fence {
		return errSuccessorAttemptExpired
	}
	if !admission.Relation().IsRoot() {
		if record.successor != admission.Relation().RootID() {
			return errSuccessorConflict
		}
		return nil
	}
	wantBudget := agent.Budget{Steps: record.request.Limits.MaxSteps, Effects: record.request.Limits.MaxEffects, Signals: record.request.Limits.MaxSignals}
	if record.successor.Valid() || admission.DeploymentRef() != record.request.DeploymentRef || admission.Budget() != wantBudget ||
		!slices.Equal(admission.Capabilities().Values(), record.request.Capabilities.Values()) {
		return errSuccessorConflict
	}
	record.prospective = admission.Relation().ProcessID()
	e.store.successors[e.predecessor] = record
	return nil
}

func (e *episodeAttempt) CommitCheckpoint(ctx context.Context, checkpoint agent.TreeCheckpoint) error {
	if !checkpoint.Valid() {
		return agent.ErrInvalidTreeSnapshot
	}
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	record := e.store.successors[e.predecessor]
	if checkpoint.Kind() != agent.TreeCheckpointStart {
		if record.successor != checkpoint.TreeSnapshot().RootID() {
			return errSuccessorConflict
		}
		return e.store.trees.CommitCheckpoint(ctx, checkpoint)
	}
	if record.fence != e.fence || record.successor.Valid() || record.prospective != checkpoint.TreeSnapshot().RootID() {
		return errSuccessorAttemptExpired
	}
	if err := e.store.trees.CommitCheckpoint(ctx, checkpoint); err != nil {
		return err
	}
	// Both writes are inside the same transaction. A lost response must reveal
	// this successor on lookup before any caller can reserve a second start.
	record.successor = record.prospective
	e.store.successors[e.predecessor] = record
	e.store.starts++
	if e.store.loseStartAcknowledgment {
		e.store.loseStartAcknowledgment = false
		return errStartAcknowledgmentLost
	}
	return nil
}

func (e *episodeAttempt) ActivateTree(ctx context.Context, activation agent.TreeActivation) error {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	if e.store.successors[e.predecessor].successor != activation.TreeSnapshot().RootID() {
		return errSuccessorConflict
	}
	return e.store.trees.ActivateTree(ctx, activation)
}

func (e *episodeAttempt) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	if e.store.successors[e.predecessor].successor != boundary.TreeSnapshot().RootID() {
		return errSuccessorConflict
	}
	return e.store.trees.CommitEffect(ctx, boundary)
}
