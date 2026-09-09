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
