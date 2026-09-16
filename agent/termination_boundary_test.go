package agent

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestPreparedFailurePreservesDispatchEvidence(t *testing.T) {
	for _, mode := range []string{"unused", "recovered", "in_flight"} {
		t.Run(mode, func(t *testing.T) {
			runtime, process := newChildCompletionTestProcess(t)
			effect, err := NewDispatcherEffect([]byte(`{"operation":"retained"}`))
			if err != nil {
				t.Fatal(err)
			}
			transition, err := Continue(0, effect)
			if err != nil {
				t.Fatal(err)
			}
			if failure := process.prepareStepResult(stepJobResult{
				transition: transition, candidate: process.execution, candidateState: process.committedExecutionState,
			}); failure != nil {
				t.Fatal(failure.cause)
			}
			record := &process.prepared.Effects[0]
			if beginErr := record.begin(); beginErr != nil {
				t.Fatal(beginErr)
			}
			if _, captureErr := runtime.captureTree(); captureErr != nil {
				t.Fatal(captureErr)
			}
			if mode == "recovered" {
				process.restoredPending = restoredPendingEffect{id: record.ID, replayPolicy: ReplayPolicyNever}
			}
			if mode == "in_flight" {
				runtime.setProcessJob(process.handle.processID, &processJob{kind: processJobDispatch, attempt: 1, effectID: record.ID})
			}
			runtime.failProcessContract(process, "engine.contract.failed", errors.New("contract failure"))
			if mode == "in_flight" {
				if process.status.Terminal() || record.Phase != effectPhasePending {
					t.Fatal("failure discarded an owned attempt before its completion")
				}
				settlement, settlementErr := NewSettlement(record.ID, SettlementStatusUnknown, []byte(`null`))
				if settlementErr != nil {
					t.Fatal(settlementErr)
				}
				runtime.applyCompletion(treeJobCompletion{
					processID: process.handle.processID, kind: processJobDispatch, attempt: 1,
					dispatch: dispatchJobResult{effectID: record.ID, settlement: settlement},
				})
			}
			runtime.advanceOne()
			var wantUnresolved []EffectID
			wantPhase := effectPhasePlanned
			if mode != "unused" {
				wantUnresolved = []EffectID{record.ID}
				wantPhase = effectPhaseSettled
			}
			if process.status != StatusFailed || record.Phase != wantPhase ||
				!slices.Equal(process.termination.UnresolvedEffectIDs(), wantUnresolved) {
				t.Fatalf("termination=%+v record=%+v", process.termination, record)
			}
			snapshot, err := runtime.captureTree()
			if err != nil {
				t.Fatal(err)
			}
			restoredEngine, err := NewEngine(EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			defer mustCloseEngine(t, restoredEngine)
			restored, err := restoredEngine.RestoreTree(t.Context(), process.deployment, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			result := mustAwait(t, restored)
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != FailureKindContract || failure.Code() != "engine.contract.failed" ||
				!slices.Equal(result.Termination().UnresolvedEffectIDs(), wantUnresolved) {
				t.Fatalf("restored termination=%+v", result.Termination())
			}
		})
	}
}

func TestTerminalOutcomeCannotBeReplacedOrRepublished(t *testing.T) {
	for _, durable := range []bool{false, true} {
		runtime, process := newChildCompletionTestProcess(t)
		if durable {
			runtime.engine.durability = &recordingTreeDurability{}
		}
		runtime.failProcessContract(process, "engine.first.failure", errors.New("first failure"))
		if durable {
			snapshot, err := runtime.captureTree()
			if err != nil {
				t.Fatal(err)
			}
			runtime.advanceHead(snapshot)
			runtime.publishAcknowledgedChanges()
		}
		before, err := runtime.captureTree()
		if err != nil {
			t.Fatal(err)
		}
		runtime.failProcessContract(process, "engine.late.failure", errors.New("late failure"))
		runtime.finishIfTerminal(process)
		runtime.publishAcknowledgedChanges()
		after, err := runtime.captureTree()
		if err != nil || !bytes.Equal(before.JSON(), after.JSON()) {
			t.Fatalf("late failure changed terminal state: %v", err)
		}
		result := mustAwait(t, &Process{handle: process.handle})
		failure, _ := result.Termination().Failure()
		if failure.Code() != "engine.first.failure" {
			t.Fatalf("terminal failure replaced: %+v", failure)
		}
	}
}

func TestProcessMembershipRetainsOwnedWork(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	job := &processJob{kind: processJobRestore, attempt: 1}
	runtime.setProcessJob(process.handle.processID, job)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("removed a Process while its job still owned a completion")
			}
		}()
		runtime.removeProcess(process.handle.processID)
	}()
	if runtime.processes[process.handle.processID] != process || runtime.jobs[process.handle.processID] != job || runtime.inFlightWork.Load() != 1 {
		t.Fatal("rejected removal changed Process ownership")
	}
	runtime.applyCompletion(treeJobCompletion{
		processID: process.handle.processID, kind: processJobRestore, attempt: 1,
		restore: restoreJobResult{execution: process.execution},
	})
	if len(runtime.jobs) != 0 || runtime.inFlightWork.Load() != 0 {
		t.Fatal("completion did not release owned work")
	}
}

func TestSignalCommitWithoutCallerStillCompletes(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	runtime.applySuccessfulTreeCommit(&treeCommit{kind: treeCommitSignals, processID: process.handle.processID})
	if _, queued := runtime.queued[process.handle.processID]; !queued {
		t.Fatal("acknowledgment did not schedule the Process")
	}
}
