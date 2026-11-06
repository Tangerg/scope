package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestRecoveryRejectsCheckpointOutputOutsideProgramContractBeforeActivation(t *testing.T) {
	for _, exited := range []bool{false, true} {
		name := "draining"
		if exited {
			name = "exited"
		}
		t.Run(name, func(t *testing.T) {
			log := integrityInitialLog(t)
			started := log.Entries[0].Record.(Started)
			terminal := Termination{output: controlValue(EncodePayload(42))}
			captured := ProcessCheckpoint{Started: started, Terminal: &terminal, Usage: Usage{CommittedSteps: 1}}
			if exited {
				captured.Exited = &Exited{Process: log.Root, Termination: terminal, At: started.At}
			}
			checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{captured}}
			cut := Log{Root: log.Root, Entries: []Entry{{LSN: 4, Epoch: 1, Record: checkpoint}}}
			if err := cut.Validate(); err != nil {
				t.Fatalf("program-independent checkpoint structure: %v", err)
			}
			store := &integrityStore{log: cut}
			runtime, program := integrityRuntime(t, store)
			if _, err := runtime.Recover(t.Context(), program, cut.Root); !errors.Is(err, ErrInvalidPayload) || store.appends != 0 {
				t.Fatalf("invalid terminal output reached activation: error=%v appends=%d", err, store.appends)
			}
		})
	}
}

func TestRecoveryRejectsRetainedChildOutputOutsideItsProgramContract(t *testing.T) {
	for _, exited := range []bool{false, true} {
		name := "draining"
		if exited {
			name = "exited"
		}
		t.Run(name, func(t *testing.T) {
			store := &integrityStore{}
			runtime := kernelRuntime(t, Config{Store: store})
			program := treeFixtureProgram(t, runtime, 1, false, false, Budget{})
			root := controlValue(ParseProcessID("child-output-contract"))
			key := controlValue(ParseChildKey("child-0"))
			parent := Started{Process: root, Program: program.ProgramRef(), Input: controlValue(EncodePayload(0)), Limits: DefaultTreeLimits(), At: time.Unix(1, 0).UTC()}
			child := Started{Process: controlValue(root.Child(key)), Program: program.children[0].ProgramRef(), Input: parent.Input, At: parent.At}
			terminal := Termination{output: controlValue(EncodePayload("outside the integer contract"))}
			checkpoint := Checkpoint{StartedProcesses: 2, Processes: []ProcessCheckpoint{
				{Started: parent, State: controlValue(EncodeExecutionState("test.tree", treeFixtureState{Spawned: true})), Children: []StartedChild{{Key: key, Started: 5}}, Usage: Usage{CommittedSteps: 1, PreparedEffects: 1, AcceptedMessages: 1}, Yielded: true},
				{Started: child, Terminal: &terminal, Usage: Usage{CommittedSteps: 1}},
			}}
			if exited {
				checkpoint.Processes[1].Exited = &Exited{Process: child.Process, Termination: terminal, At: child.At}
				checkpoint.Processes[0].Inbox = Inbox{Exit{Child: child.Process, Termination: terminal}}
				checkpoint.Processes[0].Usage.AcceptedMessages++
				checkpoint.Processes[0].Yielded = false
			}
			store.log = Log{Root: root, Entries: []Entry{{LSN: 10, Epoch: 1, Record: checkpoint}}}
			if err := store.log.Validate(); err != nil {
				t.Fatalf("program-independent checkpoint structure: %v", err)
			}
			if _, err := runtime.Recover(t.Context(), program, root); !errors.Is(err, ErrInvalidPayload) || store.appends != 0 {
				t.Fatalf("invalid child output reached activation: error=%v appends=%d", err, store.appends)
			}
		})
	}
}

func TestRecoveryAdmitsSignalsThroughTheirOwnContract(t *testing.T) {
	for _, checkpointed := range []bool{false, true} {
		name := "log replay"
		if checkpointed {
			name = "checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			for _, addressed := range []bool{false, true} {
				name := "unaddressed signal"
				if addressed {
					name = "wait answer"
				}
				t.Run(name, func(t *testing.T) {
					store := NewMemoryStore()
					runtime := kernelRuntime(t, Config{Store: store})
					schema := controlValue(jsonschema.For[int]())
					descriptor := controlValue(NewDescriptor(DescriptorConfig{
						Name: "test.signal_recovery_contract", Description: "Restore signals using their declared admission contract.",
						InputSchema: schema, OutputSchema: schema, SignalSchema: controlValue(jsonschema.For[string]()),
					}))
					waiting := controlValue(Yield(0, nil, kernelWait()))
					definition := &boundaryDefinition{descriptor: descriptor, step: func(_ context.Context, turn Turn) (Transition, error) {
						if len(turn.Messages) == 0 {
							return waiting, nil
						}
						return Complete(1, controlValue(EncodePayload(42)))
					}}
					program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
					root := controlValue(ParseProcessID("signal-recovery-contract"))
					started := Started{Process: root, Program: program.ProgramRef(), Input: controlValue(EncodePayload(0)), Limits: DefaultTreeLimits(), At: time.Unix(1, 0).UTC()}
					signal := Signal{ID: controlValue(ParseSignalID("accepted")), Payload: controlValue(EncodePayload("continue"))}
					waits := []Wait{kernelWait()}
					if addressed {
						signal.Wait, signal.Payload = kernelWait().Key, controlValue(EncodePayload(42))
						waits = nil
					}
					records := []Record{started, Activated{Epoch: 1}, Stepped{Process: root, Step: 1, Transition: waiting}, SignalAdmitted{Process: root, Signal: signal, At: started.At}}
					if checkpointed {
						records = append(records, Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{
							Started: started, State: controlValue(EncodeExecutionState("test.boundary", 0)), Inbox: Inbox{signal},
							Receipts: []SignalReceipt{signal.receipt()}, Usage: Usage{CommittedSteps: 1, AcceptedMessages: 1}, Waits: waits,
						}}})
					}
					if err := store.Append(t.Context(), Batch{Root: root, Records: records}); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
					defer cancel()
					handle := controlValue(runtime.Recover(ctx, program, root))
					result := controlValue(handle.Await(ctx))
					if output := controlValue(result.Output()); output != 42 {
						t.Fatalf("restored signal output=%d", output)
					}
				})
			}
		})
	}
}

func TestRecoveryAdmitsEachTerminalVariant(t *testing.T) {
	for _, test := range []struct {
		name        string
		termination Termination
	}{
		{"completed", Termination{output: controlValue(EncodePayload("input"))}},
		{"failed", Termination{failure: controlValue(NewFailure(FailureKindExternal, "test.external_failure", "external failure"))}},
		{"canceled", Termination{cause: TerminationCauseHostCancellation, reason: "host canceled"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := integrityInitialLog(t)
			started := log.Entries[0].Record.(Started)
			checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{
				Started: started, Terminal: &test.termination,
				Exited: &Exited{Process: log.Root, Termination: test.termination, At: started.At},
			}}}
			records := []Record{started, Activated{Epoch: 1}}
			switch test.termination.Status() {
			case StatusCompleted:
				records = append(records, Stepped{Process: log.Root, Step: 1, Transition: controlValue(Complete(0, started.Input))})
				checkpoint.Processes[0].Usage.CommittedSteps = 1
			case StatusFailed:
				records = append(records, ExecutionFailed{Process: log.Root, Failure: test.termination.failure, At: started.At})
			case StatusCanceled:
				records = append(records, ControlRequested{Origin: ControlOriginHost, Process: log.Root, Kind: ControlCancel, Reason: test.termination.Reason(), At: started.At})
			}
			records = append(records, *checkpoint.Processes[0].Exited, checkpoint)
			store := NewMemoryStore()
			if err := store.Append(t.Context(), Batch{Root: log.Root, Records: records}); err != nil {
				t.Fatal(err)
			}
			runtime, program := integrityRuntime(t, store)
			handle := controlValue(runtime.Recover(t.Context(), program, log.Root))
			result := controlValue(handle.Await(t.Context()))
			if actual := result.Termination(); !actual.equal(test.termination) {
				t.Fatalf("restored termination=%+v, want %+v", actual, test.termination)
			}
		})
	}
}

func TestRecoveryRejectsUnaddressedSignalsOutsideProgramContractBeforeActivation(t *testing.T) {
	for _, checkpointed := range []bool{false, true} {
		name := "log replay"
		if checkpointed {
			name = "checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			log := integrityInitialLog(t)
			started := log.Entries[0].Record.(Started)
			signal := Signal{ID: controlValue(ParseSignalID("forbidden")), Payload: started.Input}
			log = integrityAppend(log, SignalAdmitted{Process: log.Root, Signal: signal, At: started.At})
			if checkpointed {
				checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{
					Started: started, State: controlValue(ParseExecutionState("fixture", started.Input.JSON())),
					Inbox: Inbox{signal}, Receipts: []SignalReceipt{signal.receipt()}, Usage: Usage{AcceptedMessages: 1},
				}}}
				log.Entries = []Entry{{LSN: 4, Epoch: 1, Record: checkpoint}}
			}
			if err := log.Validate(); err != nil {
				t.Fatalf("program-independent log structure: %v", err)
			}
			store := &integrityStore{log: log}
			runtime, program := integrityRuntime(t, store)
			if _, err := runtime.Recover(t.Context(), program, log.Root); !errors.Is(err, ErrSignalRejected) || store.appends != 0 {
				t.Fatalf("invalid unaddressed signal reached activation: error=%v appends=%d", err, store.appends)
			}
		})
	}
}
