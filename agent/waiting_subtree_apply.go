package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

type preparedProcessEvent struct {
	name    string
	payload json.RawMessage
}

type preparedProcessStateChange struct {
	processID ProcessID
	source    ProcessSnapshot
	result    processSnapshotWire
	mailbox   signalMailbox
	control   pendingControl
	events    []preparedProcessEvent
}

func newPreparedProcessStateChanges(
	source TreeSnapshot,
	result TreeSnapshot,
	canceledProcessIDs []ProcessID,
	pausedProcessIDs []ProcessID,
) ([]*preparedProcessStateChange, error) {
	sourceByID := snapshotsByProcessID(source)
	resultByID := snapshotsByProcessID(result)
	if len(sourceByID) != len(resultByID) {
		return nil, ErrInvalidPreparedWaitingSubtreeCancellation
	}
	ordered := orderedPreparedProcessChanges(canceledProcessIDs, pausedProcessIDs)
	preparedChanges := make([]*preparedProcessStateChange, 0, len(ordered))
	for _, change := range ordered {
		sourceSnapshot := sourceByID[change.id]
		resultSnapshot := resultByID[change.id]
		if !sourceSnapshot.Valid() || !resultSnapshot.Valid() {
			return nil, ErrInvalidPreparedWaitingSubtreeCancellation
		}
		preparedChange, err := newPreparedProcessStateChange(
			sourceSnapshot, resultSnapshot, change.canceled,
		)
		if err != nil {
			return nil, err
		}
		preparedChanges = append(preparedChanges, preparedChange)
	}
	return preparedChanges, nil
}

type preparedProcessChange struct {
	id       ProcessID
	canceled bool
}

func snapshotsByProcessID(snapshot TreeSnapshot) map[ProcessID]ProcessSnapshot {
	byID := make(map[ProcessID]ProcessSnapshot)
	for _, processSnapshot := range snapshot.ProcessSnapshots() {
		byID[processSnapshot.ProcessID()] = processSnapshot
	}
	return byID
}

func orderedPreparedProcessChanges(canceled, paused []ProcessID) []preparedProcessChange {
	ordered := make([]preparedProcessChange, 0, len(canceled)+len(paused))
	for _, id := range canceled {
		ordered = append(ordered, preparedProcessChange{id: id, canceled: true})
	}
	for _, id := range paused {
		ordered = append(ordered, preparedProcessChange{id: id})
	}
	return ordered
}

func newPreparedProcessStateChange(
	source ProcessSnapshot,
	result ProcessSnapshot,
	canceled bool,
) (*preparedProcessStateChange, error) {
	sourceWire, err := source.wire()
	if err != nil {
		return nil, err
	}
	resultWire, err := result.wire()
	if err != nil {
		return nil, err
	}
	mailbox, err := restoreSignalMailbox(resultWire.Mailbox)
	if err != nil {
		return nil, err
	}
	control, err := pendingControlFromWire(resultWire.PendingControl)
	if err != nil {
		return nil, err
	}
	prepared := &preparedProcessStateChange{
		processID: result.ProcessID(), source: source, result: resultWire,
		mailbox: mailbox, control: control,
	}
	if canceled {
		if resultWire.Status != StatusCanceled ||
			resultWire.ProcessEventSequence != sourceWire.ProcessEventSequence+1 {
			return nil, ErrInvalidPreparedWaitingSubtreeCancellation
		}
		return prepared, nil
	}
	if err := prepared.preparePauseEvents(sourceWire, resultWire); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (p *preparedProcessStateChange) preparePauseEvents(
	source processSnapshotWire,
	result processSnapshotWire,
) error {
	if result.Status != StatusPaused || len(result.Mailbox.Signals) < len(source.Mailbox.Signals) {
		return ErrInvalidPreparedWaitingSubtreeCancellation
	}
	for _, record := range result.Mailbox.Signals[len(source.Mailbox.Signals):] {
		if _, err := ParseChildrenCompleted(record.Signal); err != nil {
			return ErrInvalidPreparedWaitingSubtreeCancellation
		}
		waitID, _ := record.Signal.WaitID()
		payload, _ := json.Marshal(signalAcceptedEventPayload{
			SignalID: record.Signal.ID().String(), WaitID: waitID.String(),
		})
		p.events = append(p.events, preparedProcessEvent{
			name: EventSignalAccepted, payload: payload,
		})
	}
	p.events = append(p.events, preparedProcessEvent{
		name: EventProcessPaused, payload: emptyEventPayload(),
	})
	if result.ProcessEventSequence != source.ProcessEventSequence+uint64(len(p.events)) {
		return ErrInvalidPreparedWaitingSubtreeCancellation
	}
	return nil
}

func (p *preparedProcessStateChange) validateSource(process *processState) error {
	if p == nil || process == nil || p.processID != process.controller.processID ||
		process.status.Terminal() {
		return ErrInvalidPreparedWaitingSubtreeCancellation
	}
	current, err := process.capture()
	if err != nil || !bytes.Equal(current.JSON(), p.source.JSON()) {
		return ErrInvalidPreparedWaitingSubtreeCancellation
	}
	return nil
}

func (p *preparedProcessStateChange) apply(ctx context.Context, process *processState) {
	result := p.result
	process.startedAt = result.StartedAt
	process.status = result.Status
	process.committedSteps = result.CommittedSteps
	process.lastStableState = result.LastStableState.clone()
	process.mailbox = p.mailbox
	process.prepared = nil
	process.currentWaitID = WaitID{}
	if result.CurrentWaitID != nil {
		process.currentWaitID = *result.CurrentWaitID
	}
	process.pauseReason = result.PauseReason
	process.pendingControl = p.control
	process.finalOutput = Output{}
	if result.Output != nil {
		process.finalOutput = *result.Output
	}
	process.finishedAt = time.Time{}
	if result.FinishedAt != nil {
		process.finishedAt = *result.FinishedAt
	}
	process.termination = Termination{}
	if result.Termination != nil {
		process.termination = *result.Termination
	}
	process.reservedBudget = result.ReservedBudget
	process.usage = result.Usage
	process.processEventSequence = result.ProcessEventSequence - uint64(len(p.events))
	if result.Status.Terminal() {
		process.processEventSequence--
	}
	process.updateView()
	for _, event := range p.events {
		process.publishEvent(ctx, event.name, EventPhaseCommitted, 0, EffectID{}, event.payload)
	}
}

func childWaitRegistrationsFromSnapshot(
	snapshot TreeSnapshot,
) ([]*childWaitRegistration, error) {
	wire, err := snapshot.wire()
	if err != nil {
		return nil, err
	}
	registrations := make([]*childWaitRegistration, 0, len(wire.ChildWaits))
	for _, encoded := range wire.ChildWaits {
		spec, err := encoded.Spec.value()
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, &childWaitRegistration{
			parent: encoded.ParentProcessID, waitID: encoded.WaitID, spec: spec,
		})
	}
	return registrations, nil
}
