package agent

import (
	"encoding/json"
	"time"
)

// The tree owner installs wait registrations around one candidate adoption.
// A rejected local transition cannot leave a live registration behind.
func (t *treeRuntime) finalizePrepared(process *processState) error {
	finalization, err := newPreparedStepFinalization(process)
	if err != nil {
		return err
	}
	if err := finalization.prepareSettlements(); err != nil {
		return err
	}
	var registered []WaitID
	committed := false
	defer func() {
		if !committed {
			for _, waitID := range registered {
				t.unregisterChildWait(waitID)
			}
		}
	}()
	for _, opened := range finalization.openedChildWaits {
		signal, satisfied, err := t.registerChildWait(process.handle.processID, opened.WaitID(), opened.Spec())
		if err != nil {
			return err
		}
		registered = append(registered, opened.WaitID())
		if satisfied {
			finalization.immediateChildSignals = append(finalization.immediateChildSignals, signal)
		}
	}
	if err := finalization.enqueueImmediateChildSignals(); err != nil {
		return err
	}
	if err := finalization.prepareTransition(time.Now().Round(0).UTC()); err != nil {
		return err
	}
	finalization.commit()
	committed = true
	for _, waitID := range finalization.consumedChildWaits {
		t.unregisterChildWait(waitID)
	}
	for _, waitID := range finalization.transition.closedChildWaits {
		t.unregisterChildWait(waitID)
	}
	t.publishEphemeralStatus(process)
	payload, _ := json.Marshal(stepCommittedEventPayload{ProcessStatus: process.status})
	t.publishEventAfterCommit(process, EventStepCommitted, EventPhaseCommitted, process.committedSteps, EffectID{}, payload)
	if process.status == StatusPaused {
		t.publishEventAfterCommit(process, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	}
	return nil
}

func (t *treeRuntime) terminatePrepared(process *processState) {
	process.prepared.candidate = nil
	process.execution = nil
	t.commitTerminationWithUnresolved(process, stepOutcome{}, process.unknownEffectIDs())
}

func (t *treeRuntime) failProcess(process *processState, kind FailureKind, code string, err error) {
	process.recordFailure(kind, code, err)
	t.commitTermination(process, stepOutcome{})
}

func (t *treeRuntime) commitTermination(process *processState, outcome stepOutcome) {
	t.commitTerminationWithUnresolved(process, outcome, nil)
}

func (t *treeRuntime) commitTerminationWithUnresolved(process *processState, outcome stepOutcome, unresolvedEffectIDs []EffectID) {
	termination := process.resolveStepTermination(outcome)
	process.installTermination(termination.withUnresolvedEffectIDs(unresolvedEffectIDs), Output{}, time.Now().Round(0).UTC())
	for _, waitID := range process.mailbox.closeAllWaits() {
		t.unregisterChildWait(waitID)
	}
	t.publishEphemeralStatus(process)
}
