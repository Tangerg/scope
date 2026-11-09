package run

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestWaitIdentitySurvivesWithdrawalAndCheckpointCuts(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		for _, checkpointed := range []bool{false, true} {
			t.Run(fmt.Sprintf("withdrawn=%t/checkpoint=%t", withdrawn, checkpointed), func(t *testing.T) {
				log := integrityInitialLog(t)
				started := log.Entries[0].Record.(Started)
				original := kernelWait()
				log = integrityAppend(log, Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, nil, original))})
				step := uint64(2)
				if withdrawn {
					other := original
					other.Key = controlValue(ParseWaitKey("another-question"))
					signal := Signal{ID: controlValue(ParseSignalID("withdraw")), Payload: started.Input}
					log = integrityAppend(log, SignalAdmitted{Process: log.Root, Signal: signal, At: started.At},
						Stepped{Process: log.Root, Step: step, Delivered: 1, Transition: controlValue(Yield(1, nil, other))})
					step++
				}
				if checkpointed {
					state := controlValue(projectLog(log))
					checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{
						state.processes[log.Root].materialize(controlValue(ParseExecutionState("fixture", started.Input.JSON()))),
					}}
					kind, encoded, err := EncodeRecord(checkpoint)
					if err != nil {
						t.Fatal(err)
					}
					decoded := controlValue(DecodeRecord(kind, encoded))
					log.Entries = []Entry{{LSN: log.Head() + 1, Epoch: log.Epoch(), Record: decoded}}
				}
				signal := Signal{ID: controlValue(ParseSignalID("redeclare")), Payload: started.Input}
				log = integrityAppend(log, SignalAdmitted{Process: log.Root, Signal: signal, At: started.At})
				for _, test := range []struct {
					name   string
					change func(*Wait)
				}{
					{name: "same contract"},
					{name: "question", change: func(w *Wait) { w.Question = controlValue(EncodePayload("Different action?")) }},
					{name: "answer schema", change: func(w *Wait) { w.AnswerSchema = controlValue(jsonschema.For[string]()) }},
					{name: "missing schema", change: func(w *Wait) { w.AnswerSchema = jsonschema.Schema{} }},
					{name: "answer size", change: func(w *Wait) { w.MaxAnswerBytes++ }},
				} {
					t.Run(test.name, func(t *testing.T) {
						candidate := original
						if test.change != nil {
							test.change(&candidate)
						}
						proposed := integrityAppend(log, Stepped{Process: log.Root, Step: step, Delivered: 1,
							Transition: controlValue(Yield(1, nil, candidate))})
						if test.change != nil {
							if err := proposed.Validate(); !errors.Is(err, ErrInvalidLog) {
								t.Fatalf("changed wait identity survived admission: %v", err)
							}
							return
						}
						state := controlValue(projectLog(proposed))
						waits := state.processes[log.Root].pendingWaits()
						if len(waits) != 1 || !waits[0].equal(original) {
							t.Fatalf("same contract did not reopen its original wait: %+v", waits)
						}
						answer := SignalAdmitted{Process: log.Root, Signal: answerSignal(original.Key, controlValue(EncodePayload(42))), At: started.At}
						if err := integrityAppend(proposed, answer).Validate(); err != nil {
							t.Fatalf("reintroduced original contract rejected its answer: %v", err)
						}
					})
				}
			})
		}
	}
}

func TestRecoveredProcessCannotRedirectAnOutstandingAnswer(t *testing.T) {
	store := NewMemoryStore()
	original := kernelWait()
	other := original
	other.Key = controlValue(ParseWaitKey("another-question"))
	changed := original
	changed.Question = controlValue(EncodePayload("Approve a different action?"))
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.wait_identity"), step: func(_ context.Context, turn Turn) (Transition, error) {
		switch turn.Step {
		case 1:
			return Yield(0, nil, original)
		case 2:
			return Yield(uint32(len(turn.Messages)), nil, other)
		default:
			return Yield(uint32(len(turn.Messages)), nil, changed)
		}
	}}
	runtime := kernelRuntime(t, Config{Store: store, CheckpointInterval: 1})
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	waitForKernelStep(t, ctx, handle, 1)
	if _, err := handle.DeliverSignals(ctx, Signal{ID: controlValue(ParseSignalID("withdraw")), Payload: controlValue(EncodePayload(0))}); err != nil {
		t.Fatal(err)
	}
	var captured bool
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if checkpoint, ok := entry.Record.(Checkpoint); ok && checkpoint.Processes[0].Usage.CommittedSteps == 2 {
			captured = true
			break
		}
	}
	if !captured {
		t.Fatal("withdrawn identity was never checkpointed")
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	replacement := kernelRuntime(t, Config{Store: recoveryCutStore{MemoryStore: store}})
	recovered := controlValue(replacement.Recover(ctx, program, handle.ID()))
	if err := recovered.Answer(ctx, original.Key, 42); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("withdrawn wait accepted an answer: %v", err)
	}
	if _, err := recovered.DeliverSignals(ctx, Signal{ID: controlValue(ParseSignalID("reintroduce")), Payload: controlValue(EncodePayload(0))}); err != nil {
		t.Fatal(err)
	}
	result := controlValue(recovered.Await(ctx))
	failure, failed := result.Termination().Failure()
	if !failed || failure.Kind() != FailureKindContract {
		t.Fatalf("changed question was published under the old key: %+v", result.Termination())
	}
}
