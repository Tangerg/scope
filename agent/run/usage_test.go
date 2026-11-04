package run

import (
	"errors"
	"math"
	"testing"
)

func TestLogUsageRetainsReapedWorkAndCountsOnlyDefiniteMessages(t *testing.T) {
	log := integrityInitialLog(t)
	parent := log.Entries[0].Record.(Started)
	key := controlValue(ParseChildKey("child"))
	child := Started{Process: controlValue(log.Root.Child(key)), Program: parent.Program, Input: parent.Input, At: parent.At}
	spawn := controlValue(NewChildStartEffect(ChildSpec{Key: key, ProgramRef: child.Program, Input: child.Input}))
	external := controlValue(NewDispatcherEffect(parent.Input.JSON()))
	spawnIntent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: spawn, Policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, At: parent.At}
	externalIntent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1, Index: 1}, Effect: external, Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: parent.At}
	signal := SignalAdmitted{Process: log.Root, Signal: Signal{ID: controlValue(ParseSignalID("input")), Payload: parent.Input}, At: parent.At}
	log = integrityAppend(log, signal,
		Stepped{Process: log.Root, Step: 1, Delivered: 1, Transition: controlValue(Yield(1, []Effect{spawn, external}))}, spawnIntent, externalIntent)
	state := controlValue(projectLog(log))
	checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{state.processes[log.Root].materialize(controlValue(ParseExecutionState("fixture", parent.Input.JSON())))}}
	checkpointIndex := len(log.Entries)
	log = integrityAppend(log, checkpoint, child,
		EffectSettled{ID: spawnIntent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, controlValue(EncodePayload(child.Process)).JSON())), At: parent.At},
		EffectSettled{ID: externalIntent.ID, Outcome: unknownSettlement(), At: parent.At})
	if got := controlValue(log.Usage()); got != (Usage{CommittedSteps: 1, PreparedEffects: 2, AcceptedMessages: 2}) {
		t.Fatalf("unknown outcome counted as a message: %+v", got)
	}
	settled := EffectSettled{ID: externalIntent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, parent.Input.JSON())), At: parent.At}
	log = integrityAppend(log, settled,
		Stepped{Process: child.Process, Step: 1, Transition: controlValue(Complete(0, child.Input))},
		Exited{Process: child.Process, Termination: Termination{output: child.Input}, At: child.At},
		Stepped{Process: log.Root, Step: 2, Delivered: 3, Transition: controlValue(Complete(3, parent.Input))},
		Reaped{Process: child.Process}, Exited{Process: log.Root, Termination: Termination{output: parent.Input}, At: parent.At})
	inspection := controlValue(log.Inspect())
	root, found := inspection.Process(log.Root)
	if !found || len(inspection.Processes) != 1 || root.Usage != (Usage{CommittedSteps: 2, PreparedEffects: 2, AcceptedMessages: 4}) {
		t.Fatalf("root accounting or reaping changed: %+v", inspection)
	}
	if got := controlValue(log.Usage()); got != (Usage{CommittedSteps: 3, PreparedEffects: 2, AcceptedMessages: 4}) {
		t.Fatalf("historical usage lost reaped work: %+v", got)
	}
	cut := Log{Root: log.Root, Entries: log.Entries[checkpointIndex:]}
	if err := cut.Validate(); err != nil {
		t.Fatalf("recovery cut is invalid: %v", err)
	}
	if got, err := cut.Usage(); !errors.Is(err, ErrInvalidLog) || got != (Usage{}) {
		t.Fatalf("recovery cut certified historical usage: %+v, %v", got, err)
	}
	if got, err := (Log{}).Usage(); !errors.Is(err, ErrInvalidLog) || got != (Usage{}) {
		t.Fatalf("invalid log certified usage: %+v, %v", got, err)
	}
	if got, err := integrityAppend(log, settled).Usage(); !errors.Is(err, ErrInvalidLog) || got != (Usage{}) {
		t.Fatalf("duplicate settlement certified usage: %+v, %v", got, err)
	}
	state = controlValue(projectLog(log))
	before := state.processes[log.Root].usage
	if err := state.apply(Entry{LSN: state.head + 1, Epoch: state.epoch, Record: settled}); !errors.Is(err, ErrInvalidLog) || state.processes[log.Root].usage != before {
		t.Fatalf("rejected settlement advanced committed usage: %+v, %v", state.processes[log.Root].usage, err)
	}
}

func TestUsageRejectsAggregateOverflow(t *testing.T) {
	for _, amount := range []Usage{{CommittedSteps: math.MaxUint64}, {PreparedEffects: math.MaxUint64}, {AcceptedMessages: math.MaxUint64}} {
		if got, err := amount.add(Usage{}); err != nil || got != amount {
			t.Fatalf("exact counter limit rejected: %+v, %v", got, err)
		}
		if got, err := amount.add(Usage{CommittedSteps: 1, PreparedEffects: 1, AcceptedMessages: 1}); !errors.Is(err, ErrCounterExhausted) || got != (Usage{}) {
			t.Fatalf("usage overflow was not rejected: %+v, %v", got, err)
		}
	}
}
