package agent_test

import (
	"context"
	"errors"

	"github.com/Tangerg/scope/agent"
)

type episodeInputDisposition string

const (
	episodeInputPending     episodeInputDisposition = "pending"
	episodeInputConsumed    episodeInputDisposition = "consumed"
	episodeInputRetained    episodeInputDisposition = "retained_at_predecessor"
	episodeInputNotAdmitted episodeInputDisposition = "not_admitted"
)

type episodeInput struct {
	root         agent.ProcessID
	recipient    agent.ProcessID
	request      agent.SignalRequest
	acknowledged bool
	disposition  episodeInputDisposition
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
