package run

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
)

func TestExhaustedWorkBudgetsFailBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		budget Budget
	}{
		{"steps", Budget{Steps: NewQuota(0)}},
		{"effects", Budget{Effects: NewQuota(0)}},
		{"messages", Budget{Messages: NewQuota(0)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := kernelRuntime(t, Config{Budget: test.budget})
			definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.budget"), step: func(context.Context, Turn) (Transition, error) {
				return Yield(0, []Effect{controlValue(NewDispatcherEffect(json.RawMessage(`0`)))})
			}}
			var calls atomic.Int32
			dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
				calls.Add(1)
				return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`0`))
			}}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
			result := controlValue(handle.Await(t.Context()))
			failure, ok := result.Termination().Failure()
			if !ok || failure.Kind() != FailureKindExecution || failure.Code() != "runtime.resource.limit_exceeded" || calls.Load() != 0 || result.Usage().PreparedEffects != 0 {
				t.Fatalf("quota became a partial dispatch: failure=%+v calls=%d usage=%+v", failure, calls.Load(), result.Usage())
			}
		})
	}
}

func TestExternalSignalCannotConsumeAReservedCompletionSlot(t *testing.T) {
	runtime := kernelRuntime(t, Config{Budget: Budget{Messages: NewQuota(1)}, TreeLimits: TreeLimits{MaxPendingMessages: 1}})
	release := make(chan struct{})
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.reservation"), step: func(_ context.Context, turn Turn) (Transition, error) {
		if turn.Step == 1 {
			return Yield(0, []Effect{controlValue(NewDispatcherEffect(json.RawMessage(`0`)))})
		}
		return Complete(1, controlValue(EncodePayload(1)))
	}}
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(ctx context.Context, _ EffectRequest, _ DeltaEmitter) (Settlement, error) {
		select {
		case <-release:
			return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`0`))
		case <-ctx.Done():
			return Settlement{}, ctx.Err()
		}
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	view := controlValue(handle.Inspect(ctx))
	before := view.Head
	if accepted, err := handle.DeliverSignals(ctx, Signal{ID: controlValue(ParseSignalID("extra")), Payload: controlValue(EncodePayload(2))}); accepted || !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("reserved slot was stolen: accepted=%t error=%v", accepted, err)
	}
	if after := controlValue(handle.Inspect(ctx)); after.Head != before {
		t.Fatal("rejected input advanced the durable log")
	}
	close(release)
	result := controlValue(handle.Await(ctx))
	if output, err := result.Output(); err != nil || output != 1 || result.Usage().AcceptedMessages != 1 {
		t.Fatalf("admitted effect could not finish: output=%d error=%v usage=%+v", output, err, result.Usage())
	}
}

func TestCheckpointByteBoundStopsTheWriterWithoutInventingProgress(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store, CheckpointInterval: 1, TreeLimits: TreeLimits{MaxCheckpointBytes: NewQuota(1)}})
	program := treeFixtureProgram(t, runtime, 0, true, false, Budget{})
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	_, err := handle.Await(ctx)
	if !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("oversized checkpoint error=%v", err)
	}
	log, _, err := store.Load(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found := log.LatestCheckpoint(); found {
		t.Fatal("oversized checkpoint reached storage")
	}
	if err := log.Validate(); err != nil {
		t.Fatalf("checkpoint rejection damaged acknowledged progress: %v", err)
	}
	if log.Head() != 3 {
		t.Fatalf("checkpoint rejection wrote extra progress: head=%d", log.Head())
	}
}

func TestChildAdmissionCannotCreateWorkAuthority(t *testing.T) {
	read := controlValue(ParseCapability("resource.read"))
	for _, test := range []struct {
		name         string
		parentBudget Budget
		childBudget  Budget
		parentGrants CapabilitySet
		childGrants  CapabilitySet
		limits       TreeLimits
		admitted     bool
	}{
		{"finite grant", Budget{Steps: NewQuota(10), Effects: NewQuota(10), Messages: NewQuota(10)}, Budget{Steps: NewQuota(1), Effects: NewQuota(0), Messages: NewQuota(0)}, controlValue(NewCapabilitySet(read)), controlValue(NewCapabilitySet(read)), TreeLimits{}, true},
		{"unlimited child under finite parent", Budget{Steps: NewQuota(10)}, Budget{}, CapabilitySet{}, CapabilitySet{}, TreeLimits{}, false},
		{"steps already spent by parent", Budget{Steps: NewQuota(4)}, Budget{Steps: NewQuota(4)}, CapabilitySet{}, CapabilitySet{}, TreeLimits{}, false},
		{"effects already spent by parent", Budget{Effects: NewQuota(3)}, Budget{Effects: NewQuota(3)}, CapabilitySet{}, CapabilitySet{}, TreeLimits{}, false},
		{"messages reserved for completion and exit", Budget{Messages: NewQuota(4)}, Budget{Messages: NewQuota(4)}, CapabilitySet{}, CapabilitySet{}, TreeLimits{}, false},
		{"capability escalation", Budget{}, Budget{}, CapabilitySet{}, controlValue(NewCapabilitySet(read)), TreeLimits{}, false},
		{"parent disallows children", Budget{}, Budget{}, CapabilitySet{}, CapabilitySet{}, TreeLimits{MaxChildren: NewQuota(0)}, false},
		{"tree contains only root", Budget{}, Budget{}, CapabilitySet{}, CapabilitySet{}, TreeLimits{MaxTreeProcesses: NewQuota(1)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			admissions := make(chan ProcessAdmission, 2)
			var child Program[int, int]
			runtime := kernelRuntime(t, Config{Store: store, Budget: test.parentBudget, Capabilities: test.parentGrants, TreeLimits: test.limits,
				ProgramResolver: recoveryResolver(func(reference ProgramRef) (Program[Payload, Payload], error) {
					if reference != child.ProgramRef() {
						return Program[Payload, Payload]{}, ErrInvalidProgram
					}
					return child.Erase(), nil
				}), ProcessAdmitter: ProcessAdmitterFunc(func(_ context.Context, admission ProcessAdmission) error {
					admissions <- admission
					return nil
				}),
			})
			child = treeFixtureProgram(t, runtime, 0, false, false, Budget{})
			spec := ChildSpec{Key: controlValue(ParseChildKey("grant")), ProgramRef: child.ProgramRef(), Input: controlValue(EncodePayload(0)), Budget: test.childBudget, Capabilities: test.childGrants}
			definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.grant"), step: func(_ context.Context, turn Turn) (Transition, error) {
				if turn.Step == 1 {
					return Yield(0, []Effect{controlValue(NewChildStartEffect(spec))})
				}
				for _, message := range turn.Messages {
					if completion, ok := message.(Completion); ok {
						admitted := 0
						if completion.Outcome.Status() == SettlementStatusSucceeded {
							admitted = 1
						}
						return Complete(uint32(len(turn.Messages)), controlValue(EncodePayload(admitted)))
					}
				}
				return Transition{}, ErrInvalidMessage
			}}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Configuration: spec, Revision: "one"}))
			id := foregroundStartID()
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: id}))
			result := controlValue(handle.Await(t.Context()))
			output := controlValue(result.Output())
			if (output == 1) != test.admitted {
				t.Fatalf("child admitted=%t want=%t", output == 1, test.admitted)
			}
			root := <-admissions
			start, isRoot := root.StartID()
			if !isRoot || start != id || root.ProgramRef() != program.ProgramRef() || root.Descriptor().Digest() != program.Descriptor().Digest() || root.Budget() != test.parentBudget || !root.Capabilities().Allows(test.parentGrants) {
				t.Fatal("host admission did not receive the root's exact declaration")
			}
			if test.admitted {
				admitted := <-admissions
				if _, isRoot := admitted.StartID(); isRoot || admitted.ProgramRef() != child.ProgramRef() || admitted.Budget() != spec.Budget || !admitted.Capabilities().Allows(spec.Capabilities) {
					t.Fatal("child acquired another grant or root identity")
				}
			} else if len(admissions) != 0 {
				t.Fatal("an unfunded child reached the host admission boundary")
			}
			log, _, err := store.Load(t.Context(), handle.ID())
			if err != nil || log.Validate() != nil {
				t.Fatalf("grant decision did not produce a valid durable history: %v", err)
			}
		})
	}
}
