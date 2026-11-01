package run_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"

	"github.com/Tangerg/scope/agent/run"
)

var (
	errSuccessorConflict       = errors.New("successor request conflicts with its predecessor admission")
	errSuccessorAttemptExpired = errors.New("successor initialization attempt was fenced")
	errStartAcknowledgmentLost = errors.New("successor start acknowledgment lost")
	errUnsafeEpisodeBoundary   = errors.New("episode has no safe continuation boundary")
)

type successorRecord struct {
	request     successorRequest
	digest      run.Digest
	fence       uint64
	prospective run.ProcessID
	successor   run.ProcessID
}

// The mutex models one database transaction over input cutover, successor
// admission, and tree creation. The underlying tree adapter retains its own
// canonical checkpoint and incarnation rules instead of reimplementing them.
type episodeStore struct {
	mu                      sync.Mutex
	trees                   *run.MemoryStore
	sealed                  map[run.ProcessID]run.TreeSnapshot
	inputs                  map[run.SignalID]episodeInput
	successors              map[run.ProcessID]successorRecord
	allocations             uint32
	starts                  uint32
	loseStartAcknowledgment bool
}

func newEpisodeStore() *episodeStore {
	return &episodeStore{
		trees: run.NewMemoryStore(), sealed: make(map[run.ProcessID]run.TreeSnapshot),
		inputs: make(map[run.SignalID]episodeInput), successors: make(map[run.ProcessID]successorRecord),
	}
}

func (e *episodeStore) claim(request successorRequest) (*episodeAttempt, run.ProcessID, error) {
	digest, err := request.identity()
	if err != nil {
		return nil, run.ProcessID{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.sealed[request.Predecessor].Valid() {
		return nil, run.ProcessID{}, errUnsafeEpisodeBoundary
	}
	record, present := e.successors[request.Predecessor]
	if present && record.digest != digest {
		return nil, run.ProcessID{}, errSuccessorConflict
	}
	if !present {
		record = successorRecord{request: request, digest: digest}
		e.allocations++
	}
	if !record.successor.Valid() {
		if record.fence == math.MaxUint64 {
			return nil, run.ProcessID{}, errSuccessorAttemptExpired
		}
		record.fence++
		record.prospective = run.ProcessID{}
	}
	e.successors[request.Predecessor] = record
	return &episodeAttempt{store: e, predecessor: request.Predecessor, fence: record.fence}, record.successor, nil
}

// Binding precedes submission. A sealed route rejects new bindings; an existing
// delivery retains its original Process and WaitID even after successor creation.
func (e *episodeStore) bindInput(process *run.Handle[run.Payload], request run.SignalRequest) error {
	if !process.Relation().ProcessID().Valid() || !request.Valid() {
		return run.ErrInvalidSignalRequest
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if previous, present := e.inputs[request.ID()]; present {
		previousWait, _ := previous.request.WaitID()
		requestedWait, _ := request.WaitID()
		if previous.recipient != process.Relation().ProcessID() || previousWait != requestedWait || run.ComputeDigest(previous.request.Payload()) != run.ComputeDigest(request.Payload()) {
			return run.ErrSignalConflict
		}
		return nil
	}
	if e.sealed[process.Relation().RootID()].Valid() {
		return errUnsafeEpisodeBoundary
	}
	e.inputs[request.ID()] = episodeInput{root: process.Relation().RootID(), recipient: process.Relation().ProcessID(), request: request, disposition: episodeInputPending}
	return nil
}

func (e *episodeStore) acknowledgeInput(recipient run.ProcessID, id run.SignalID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	record, present := e.inputs[id]
	if !present || record.recipient != recipient || record.disposition == episodeInputNotAdmitted {
		return run.ErrSignalConflict
	}
	record.acknowledged = true
	e.inputs[id] = record
	return nil
}

// The initial continuation contract requires a successful root, all local work
// drained, and no unresolved external Effects anywhere in the tree. Retained
// inputs stay at the predecessor; this example does not transfer live work.
func (e *episodeStore) sealEpisode(ctx context.Context, process *run.Handle[run.Payload]) (run.Result[run.Payload], error) {
	if !process.Relation().IsRoot() {
		return run.Result[run.Payload]{}, errUnsafeEpisodeBoundary
	}
	result, err := process.Await(ctx)
	if err != nil {
		return run.Result[run.Payload]{}, err
	}
	if result.Termination().Status() != run.StatusCompleted {
		return run.Result[run.Payload]{}, errUnsafeEpisodeBoundary
	}
	if joinErr := process.Join(ctx); joinErr != nil {
		return run.Result[run.Payload]{}, joinErr
	}
	head, present, err := e.trees.LoadTree(ctx, process.Relation().ProcessID())
	if err != nil {
		return run.Result[run.Payload]{}, err
	}
	if !present || head.RootID() != process.Relation().ProcessID() {
		return run.Result[run.Payload]{}, errUnsafeEpisodeBoundary
	}
	for _, snapshot := range head.ProcessSnapshots() {
		if !snapshot.Status().Terminal() || len(snapshot.UnknownEffectIDs()) != 0 {
			return run.Result[run.Payload]{}, errUnsafeEpisodeBoundary
		}
		if snapshot.Relation().ProcessID() == process.Relation().ProcessID() && snapshot.Status() != run.StatusCompleted {
			return run.Result[run.Payload]{}, errUnsafeEpisodeBoundary
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sealed[process.Relation().ProcessID()].Valid() {
		return result, nil
	}
	resolved := make(map[run.SignalID]episodeInput)
	for id, record := range e.inputs {
		if record.root != process.Relation().ProcessID() {
			continue
		}
		record.disposition = episodeInputNotAdmitted
		for _, snapshot := range head.ProcessSnapshots() {
			if snapshot.Relation().ProcessID() != record.recipient {
				continue
			}
			for _, receipt := range snapshot.SignalReceipts() {
				if receipt.ID() != id {
					continue
				}
				if !receipt.Matches(record.request) {
					return run.Result[run.Payload]{}, run.ErrSignalConflict
				}
				record.disposition = episodeInputRetained
				if receipt.Consumed() {
					record.disposition = episodeInputConsumed
				}
			}
		}
		if record.acknowledged && record.disposition == episodeInputNotAdmitted {
			return run.Result[run.Payload]{}, errors.New("acknowledged input absent from final authoritative tree")
		}
		resolved[id] = record
	}
	for id, record := range resolved {
		e.inputs[id] = record
	}
	e.sealed[process.Relation().ProcessID()] = head
	return result, nil
}

type episodeAttempt struct {
	store       *episodeStore
	predecessor run.ProcessID
	fence       uint64
}

func (e *episodeAttempt) Admit(_ context.Context, admission run.ProcessAdmission) error {
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
	wantBudget := record.request.Budget
	if record.successor.Valid() || admission.ProgramRef() != record.request.ProgramRef || admission.Budget() != wantBudget ||
		!slices.Equal(admission.Capabilities().Values(), record.request.Capabilities.Values()) {
		return errSuccessorConflict
	}
	record.prospective = admission.Relation().ProcessID()
	e.store.successors[e.predecessor] = record
	return nil
}

func (e *episodeAttempt) CommitCheckpoint(ctx context.Context, checkpoint run.TreeCheckpoint) error {
	if !checkpoint.Valid() {
		return run.ErrInvalidTreeSnapshot
	}
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	record := e.store.successors[e.predecessor]
	if checkpoint.Kind() != run.TreeCheckpointKindStart {
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

func (e *episodeAttempt) ActivateTree(ctx context.Context, activation run.TreeActivation) error {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	if e.store.successors[e.predecessor].successor != activation.TreeSnapshot().RootID() {
		return errSuccessorConflict
	}
	return e.store.trees.ActivateTree(ctx, activation)
}

func (e *episodeAttempt) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	if e.store.successors[e.predecessor].successor != boundary.TreeSnapshot().RootID() {
		return errSuccessorConflict
	}
	return e.store.trees.CommitEffect(ctx, boundary)
}

func (e *episodeAttempt) LookupStart(ctx context.Context, id run.StartID) (run.StartReceipt, bool, error) {
	return e.store.trees.LookupStart(ctx, id)
}
