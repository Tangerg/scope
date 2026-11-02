package run

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestInvalidStepCandidatesCannotPublishProgress(t *testing.T) {
	for _, test := range []struct {
		name string
		step func(context.Context, Turn) (Transition, error)
	}{
		{"absent transition", func(context.Context, Turn) (Transition, error) { return Transition{}, nil }},
		{"consumes undelivered message", func(context.Context, Turn) (Transition, error) { return Complete(1, controlValue(EncodePayload(0))) }},
		{"wrong output contract", func(context.Context, Turn) (Transition, error) {
			return Complete(0, controlValue(EncodePayload("wrong type")))
		}},
		{"yield without a wake source", func(context.Context, Turn) (Transition, error) { return Yield(0, nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store})
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
				Definition: &boundaryDefinition{descriptor: kernelDescriptor(t, "test.invalid_step"), step: test.step}, Revision: "one",
			}))
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
			result := controlValue(handle.Await(t.Context()))
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != FailureKindContract || failure.Code() != failureCodeExecutionTransitionInvalid {
				t.Fatalf("malformed Step did not fail at its contract: %+v", failure)
			}
			log, _, err := store.Load(t.Context(), handle.ID())
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range log.Entries {
				switch entry.Record.(type) {
				case Stepped, EffectIntent:
					t.Fatal("invalid candidate became committed progress")
				}
			}
		})
	}
}

type policyProbe struct {
	policy   func(Effect) EffectPolicy
	dispatch func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error)
}

func (p policyProbe) Policy(effect Effect) EffectPolicy { return p.policy(effect) }
func (p policyProbe) Dispatch(ctx context.Context, request EffectRequest, emitter DeltaEmitter) (Settlement, error) {
	return p.dispatch(ctx, request, emitter)
}

func TestInvalidEffectPolicyRejectsTheWholeStepBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name       string
		policy     func(Effect) EffectPolicy
		wantKind   FailureKind
		wantCode   string
		dispatcher bool
	}{
		{"missing dispatcher", nil, FailureKindContract, failureCodeExecutionEffectInvalid, false},
		{"invalid replay policy", func(Effect) EffectPolicy { return EffectPolicy{} }, FailureKindContract, failureCodeExecutionEffectInvalid, true},
		{"policy panic", func(Effect) EffectPolicy { panic("policy failed") }, FailureKindPanic, failureCodeExecutionEffectInvalid, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store})
			var calls atomic.Int32
			var dispatcher Dispatcher
			if test.dispatcher {
				dispatcher = policyProbe{policy: test.policy, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
					calls.Add(1)
					return NewSettlement(SettlementStatusSucceeded, []byte(`7`))
				}}
			}
			program := lifecycleCallProgram(t, runtime, dispatcher)
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
			result := controlValue(handle.Await(t.Context()))
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != test.wantKind || failure.Code() != test.wantCode || calls.Load() != 0 {
				t.Fatalf("invalid policy crossed the dispatch boundary: failure=%+v calls=%d", failure, calls.Load())
			}
			log, _, err := store.Load(t.Context(), handle.ID())
			if err != nil {
				t.Fatal(err)
			}
			if log.Head() != 4 || result.Usage().CommittedSteps != 0 || result.Usage().PreparedEffects != 0 {
				t.Fatalf("invalid policy committed partial progress: head=%d usage=%+v", log.Head(), result.Usage())
			}
		})
	}
}

func TestDispatchDefectsRemainUncertainUntilExplicitResolution(t *testing.T) {
	cause := errors.New("external response unavailable")
	for _, test := range []struct {
		name     string
		dispatch func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error)
		kind     FailureKind
	}{
		{"invalid settlement", func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) { return Settlement{}, nil }, FailureKindContract},
		{"transport error", func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) { return Settlement{}, cause }, FailureKindExternal},
		{"dispatch panic", func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) { panic(cause) }, FailureKindPanic},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := kernelRuntime(t, Config{})
			program := lifecycleCallProgram(t, runtime, boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: test.dispatch})
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			id := lifecycleUnknown(t, ctx, handle)
			inspection := controlValue(handle.Inspect(ctx))
			process, found := inspection.Process(handle.ID())
			if !found || process.Status.Terminal() || len(process.PendingEffects) != 1 || process.PendingEffects[0].Failure == nil || process.PendingEffects[0].Failure.Kind() != test.kind || process.Usage.AcceptedMessages != 0 {
				t.Fatalf("uncertain dispatch invented a completion: %+v", process)
			}
			if err := handle.ReplayUnknownEffect(ctx, id); !errors.Is(err, ErrEffectReplayForbidden) {
				t.Fatalf("Never effect was replayed: %v", err)
			}
			if err := handle.ResolveUnknownEffect(ctx, id, controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`7`)))); err != nil {
				t.Fatal(err)
			}
			result := controlValue(handle.Await(ctx))
			if value, err := result.Output(); err != nil || value != 7 || result.Usage().AcceptedMessages != 1 {
				t.Fatalf("definite resolution was not delivered once: value=%d error=%v usage=%+v", value, err, result.Usage())
			}
		})
	}
}

func TestRecoveryUsesTheCommittedReplayPolicy(t *testing.T) {
	for _, replay := range []ReplayPolicy{ReplayPolicyNever, ReplayPolicySameIdentity} {
		t.Run(replay.String(), func(t *testing.T) {
			store := NewMemoryStore()
			cause := errors.New("settlement write interrupted")
			fault := &lifecycleFaultStore{store: store, kind: RecordKindEffectSettled, cause: cause}
			runtime := kernelRuntime(t, Config{Store: fault})
			var policies, dispatches atomic.Int32
			dispatcher := policyProbe{policy: func(Effect) EffectPolicy {
				if policies.Add(1) != 1 {
					panic("recovery queried an uncommitted policy")
				}
				return EffectPolicy{Replay: replay}
			}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
				dispatches.Add(1)
				return NewSettlement(SettlementStatusSucceeded, []byte(`7`))
			}}
			program := lifecycleCallProgram(t, runtime, dispatcher)
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			if _, err := handle.Await(ctx); !errors.Is(err, cause) {
				t.Fatal(err)
			}
			replacement := kernelRuntime(t, Config{Store: store})
			recovered := controlValue(replacement.Recover(ctx, program, handle.ID()))
			wantDispatches := int32(2)
			if replay == ReplayPolicyNever {
				wantDispatches = 1
				id := lifecycleUnknown(t, ctx, recovered)
				if err := recovered.ResolveUnknownEffect(ctx, id, controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`7`)))); err != nil {
					t.Fatal(err)
				}
			}
			if result := controlValue(recovered.Await(ctx)); !result.Valid() || policies.Load() != 1 || dispatches.Load() != wantDispatches {
				t.Fatalf("recovery changed the accepted replay decision: policy calls=%d dispatches=%d", policies.Load(), dispatches.Load())
			}
		})
	}
}

func TestRepeatedUnknownReplayDoesNotResolveItself(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	var calls atomic.Int32
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
		calls.Add(1)
		return NewSettlement(SettlementStatusUnknown, []byte(`null`))
	}}
	program := lifecycleCallProgram(t, runtime, dispatcher)
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	id := lifecycleUnknown(t, ctx, handle)
	if err := handle.ReplayUnknownEffect(ctx, id); !errors.Is(err, ErrEffectOutcomeUnknown) || calls.Load() != 2 {
		t.Fatalf("unknown replay acknowledgment=%v calls=%d", err, calls.Load())
	}
	if err := handle.ResolveUnknownEffect(ctx, id, controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`7`)))); err != nil {
		t.Fatal(err)
	}
	if result := controlValue(handle.Await(ctx)); result.Usage().PreparedEffects != 1 || result.Usage().AcceptedMessages != 1 {
		t.Fatalf("replay acquired a second lifecycle: %+v", result.Usage())
	}
}

func TestLocalWorkerCancellationCannotInventHostControl(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	program := treeFixtureProgram(t, runtime, 0, true, false, Budget{})
	started := Started{Process: hostStartID().Root(), Program: program.ProgramRef(), Input: controlValue(EncodePayload(0)), Limits: runtime.limits, At: canonicalTime(time.Now())}
	state := newTreeState(started.Process)
	tree := newTreeRuntime(runtime, state, t.Context())
	defer tree.cancel()
	if err := tree.append(t.Context(), []Record{started, Activated{Epoch: 1}}); err != nil {
		t.Fatal(err)
	}
	tree.stop()
	if err := tree.contextCanceled(); err != nil {
		t.Fatal(err)
	}
	log, _, err := store.Load(t.Context(), started.Process)
	if err != nil || log.Head() != 2 {
		t.Fatalf("local shutdown became durable cancellation: head=%d error=%v", log.Head(), err)
	}
}

func TestWriterFailureJoinsOtherDispatchesWithoutInventingTheirOutcomes(t *testing.T) {
	store := NewMemoryStore()
	cause := errors.New("settlement storage failed")
	fault := &lifecycleFaultStore{store: store, kind: RecordKindEffectSettled, cause: cause}
	runtime := kernelRuntime(t, Config{Store: fault})
	allStarted := make(chan struct{})
	var calls atomic.Int32
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(ctx context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
		calls.Add(1)
		if request.ID().Index == 1 {
			close(allStarted)
			<-ctx.Done()
			return Settlement{}, ctx.Err()
		}
		select {
		case <-allStarted:
			return NewSettlement(SettlementStatusSucceeded, []byte(`1`))
		case <-ctx.Done():
			return Settlement{}, ctx.Err()
		}
	}}
	effect := controlValue(NewDispatcherEffect([]byte(`0`)))
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.writer_failure"), step: func(_ context.Context, turn Turn) (Transition, error) {
		if turn.Step == 1 {
			return Yield(0, []Effect{effect, effect})
		}
		if len(turn.Messages) != 2 {
			return Yield(0, nil)
		}
		return Complete(2, controlValue(EncodePayload(7)))
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	_, err := handle.Await(ctx)
	runtimeError, stopped := errors.AsType[*RuntimeError](err)
	if !stopped || !errors.Is(err, cause) || len(runtimeError.UnresolvedEffectIDs()) != 2 || calls.Load() != 2 {
		t.Fatalf("writer failure lost pending dispatch ownership: calls=%d error=%v", calls.Load(), err)
	}
	log, _, err := store.Load(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range log.Entries {
		switch entry.Record.(type) {
		case EffectSettled, Exited:
			t.Fatal("stopped writer invented durable outcomes")
		}
	}
	replacement := kernelRuntime(t, Config{Store: store})
	recovered := controlValue(replacement.Recover(ctx, program, handle.ID()))
	unknown := 0
	for entry, err := range recovered.Observe(ctx, log.Head()) {
		if err != nil {
			t.Fatal(err)
		}
		if settled, ok := entry.Record.(EffectSettled); ok && settled.Outcome.Status() == SettlementStatusUnknown {
			unknown++
			if unknown == 2 {
				break
			}
		}
	}
	for _, id := range runtimeError.UnresolvedEffectIDs() {
		if err := recovered.ResolveUnknownEffect(ctx, id, controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`1`)))); err != nil {
			t.Fatal(err)
		}
	}
	result := controlValue(recovered.Await(ctx))
	if controlValue(result.Output()) != 7 || calls.Load() != 2 || result.Usage().PreparedEffects != 2 || result.Usage().AcceptedMessages != 2 {
		t.Fatalf("recovery repeated work or delivered uncertain outcomes: calls=%d usage=%+v", calls.Load(), result.Usage())
	}
}
