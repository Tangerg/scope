package run

import (
	"context"
	"errors"
	"iter"
	"testing"
)

type dynamicChildDefinition struct {
	descriptor Descriptor
	child      ProgramRef
}

type recoveryResolver func(ProgramRef) (Program[Payload, Payload], error)

func (r recoveryResolver) Resolve(reference ProgramRef) (Program[Payload, Payload], error) {
	return r(reference)
}

func (d *dynamicChildDefinition) Descriptor() Descriptor                   { return d.descriptor }
func (*dynamicChildDefinition) ChildPrograms() []Program[Payload, Payload] { return nil }
func (d *dynamicChildDefinition) Start(Payload) (Execution, error) {
	return &dynamicChildExecution{definition: d}, nil
}
func (d *dynamicChildDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	exited, err := state.Decode[bool]("test.dynamic_child")
	if err != nil {
		return nil, err
	}
	return &dynamicChildExecution{definition: d, exited: exited}, nil
}

type dynamicChildExecution struct {
	definition *dynamicChildDefinition
	exited     bool
}

func (d *dynamicChildExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("test.dynamic_child", d.exited)
}
func (d *dynamicChildExecution) Step(_ context.Context, turn Turn) (Transition, error) {
	if turn.Step == 1 {
		spawn := controlValue(NewChildStartEffect(ChildSpec{Key: controlValue(ParseChildKey("temporary")), ProgramRef: d.definition.child, Input: controlValue(EncodePayload(0))}))
		return Yield(0, []Effect{spawn})
	}
	for _, message := range turn.Messages {
		switch value := message.(type) {
		case Exit:
			d.exited = true
		case Signal:
			return Complete(uint32(len(turn.Messages)), value.Payload)
		}
	}
	if d.exited {
		return Yield(uint32(len(turn.Messages)), nil, kernelWait())
	}
	return Yield(uint32(len(turn.Messages)), nil)
}

func TestCheckpointRecoveryDoesNotRequireReapedProgramImplementations(t *testing.T) {
	store := NewMemoryStore()
	var child Program[int, int]
	runtime := kernelRuntime(t, Config{Store: store, CheckpointInterval: 1, ProgramResolver: recoveryResolver(func(reference ProgramRef) (Program[Payload, Payload], error) {
		if reference != child.ProgramRef() {
			return Program[Payload, Payload]{}, ErrInvalidProgram
		}
		return child.Erase(), nil
	})})
	child = treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	definition := &dynamicChildDefinition{descriptor: kernelDescriptor(t, "test.dynamic_parent"), child: child.ProgramRef()}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one", Configuration: definition.child}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	var cut LSN
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if checkpoint, ok := entry.Record.(Checkpoint); ok && checkpoint.StartedProcesses == 2 && len(checkpoint.Processes) == 1 && len(checkpoint.Processes[0].Waits) == 1 {
			cut = entry.LSN
			break
		}
	}
	if cut == 0 {
		t.Fatal("the reaped child never left the checkpoint")
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	replacement := kernelRuntime(t, Config{Store: store})
	recovered, err := replacement.Recover(ctx, program, handle.ID())
	if err != nil {
		t.Fatalf("reaped history required a retired program implementation: %v", err)
	}
	inspection := controlValue(recovered.Inspect(ctx))
	if len(inspection.Processes) != 1 {
		t.Fatal("recovery resurrected a reaped child")
	}
	if err := recovered.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	if result := controlValue(recovered.Await(ctx)); controlValue(result.Output()) != 42 {
		t.Fatal("live state did not resume from the checkpoint")
	}
}

type contextValueKey struct{}

type contextDefinition struct{ descriptor Descriptor }

func (c contextDefinition) Descriptor() Descriptor                   { return c.descriptor }
func (contextDefinition) ChildPrograms() []Program[Payload, Payload] { return nil }
func (contextDefinition) Start(Payload) (Execution, error)           { return contextExecution{}, nil }
func (contextDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	if ctx.Value(contextValueKey{}) != nil {
		return nil, errors.New("host context leaked into restoration")
	}
	if _, err := state.Decode[bool]("test.context"); err != nil {
		return nil, err
	}
	return contextExecution{}, nil
}

type contextExecution struct{}

func (contextExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("test.context", true)
}
func (contextExecution) Step(ctx context.Context, turn Turn) (Transition, error) {
	if ctx.Value(contextValueKey{}) != nil {
		return Transition{}, errors.New("host context became an unrecorded Step input")
	}
	if len(turn.Messages) == 0 {
		return Yield(0, nil, kernelWait())
	}
	return Complete(1, turn.Messages[0].(Signal).Payload)
}

func TestHostContextValuesCannotChangeLiveOrRecoveredSteps(t *testing.T) {
	for _, interval := range []uint64{1, defaultCheckpointInterval} {
		name := "log replay"
		if interval == 1 {
			name = "checkpoint restore"
		}
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store, CheckpointInterval: interval})
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: contextDefinition{descriptor: kernelDescriptor(t, "test.context")}, Revision: "one"}))
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), contextValueKey{}, "first host"), kernelTestTimeout)
			defer cancel()
			handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: foregroundStartID()}))
			waitForKernelStep(t, ctx, handle, 1)
			if err := runtime.Close(ctx); err != nil {
				t.Fatal(err)
			}
			replacement := kernelRuntime(t, Config{Store: store})
			recovered := controlValue(replacement.Recover(context.WithValue(ctx, contextValueKey{}, "second host"), program, handle.ID()))
			if err := recovered.Answer(ctx, kernelWait().Key, 11); err != nil {
				t.Fatal(err)
			}
			if result := controlValue(recovered.Await(ctx)); controlValue(result.Output()) != 11 {
				t.Fatal("host metadata changed deterministic progress")
			}
		})
	}
}

type simultaneousStartStore struct {
	store   *MemoryStore
	arrived chan struct{}
	release <-chan struct{}
}

func (s *simultaneousStartStore) Load(ctx context.Context, root ProcessID) (Log, bool, error) {
	return s.store.Load(ctx, root)
}
func (s *simultaneousStartStore) Read(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
	return s.store.Read(ctx, root, after)
}
func (s *simultaneousStartStore) Append(ctx context.Context, batch Batch) error {
	if batch.After == 0 {
		s.arrived <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.store.Append(ctx, batch)
}

func TestSeparateRuntimesResolveConcurrentStartAtTheLog(t *testing.T) {
	for _, conflicting := range []bool{false, true} {
		name := "same intent"
		if conflicting {
			name = "conflicting intent"
		}
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			store := &simultaneousStartStore{store: NewMemoryStore(), arrived: make(chan struct{}, 2), release: release}
			left, right := kernelRuntime(t, Config{Store: store}), kernelRuntime(t, Config{Store: store})
			program := treeFixtureProgram(t, left, 0, true, false, Budget{})
			id := foregroundStartID()
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			type submission struct {
				handle *Handle[int]
				err    error
			}
			completed := make(chan submission, 2)
			for index, runtime := range []*Runtime{left, right} {
				input := 0
				if conflicting {
					input = index
				}
				go func() {
					handle, err := runtime.Start(ctx, program, StartRequest[int]{ID: id, Input: input})
					completed <- submission{handle, err}
				}()
			}
			for range 2 {
				select {
				case <-store.arrived:
				case <-ctx.Done():
					close(release)
					t.Fatal(ctx.Err())
				}
			}
			close(release)
			conflicts := 0
			for range 2 {
				response := <-completed
				if response.err != nil {
					if _, ok := errors.AsType[*StartConflictError](response.err); !conflicting || !ok {
						t.Fatal(response.err)
					}
					conflicts++
					continue
				}
				if response.handle.ID() != id.Root() || response.handle.Epoch() != 1 {
					t.Fatal("duplicate submission created another root or writer epoch")
				}
			}
			if conflicting && conflicts != 1 || !conflicting && conflicts != 0 {
				t.Fatalf("start conflicts=%d", conflicts)
			}
			log, _, err := store.Load(ctx, id.Root())
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			for _, entry := range log.Entries {
				if _, started := entry.Record.(Started); started {
					starts++
				}
			}
			if starts != 1 || log.Epoch() != 1 {
				t.Fatalf("storage admitted competing owners: starts=%d epoch=%d", starts, log.Epoch())
			}
		})
	}
}

func TestUsageWireKeepsExplicitZeroCounters(t *testing.T) {
	usage := Usage{}
	if err := usage.UnmarshalJSON([]byte(`{"committed_steps":0,"prepared_effects":0,"accepted_messages":0}`)); err != nil {
		t.Fatal(err)
	}
	if usage != (Usage{}) {
		t.Fatal("zero counters gained usage")
	}
	for _, invalid := range []string{`{}`, `{"committed_steps":0,"prepared_effects":0}`, `{"committed_steps":0,"prepared_effects":0,"accepted_messages":0,"settled":1}`} {
		if err := usage.UnmarshalJSON([]byte(invalid)); err == nil {
			t.Fatalf("incomplete or competing usage representation accepted: %s", invalid)
		}
	}
}

type publicationStore struct {
	store    *MemoryStore
	accepted chan struct{}
	release  <-chan struct{}
}

func (p publicationStore) Load(ctx context.Context, root ProcessID) (Log, bool, error) {
	return p.store.Load(ctx, root)
}
func (p publicationStore) Read(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
	return p.store.Read(ctx, root, after)
}
func (p publicationStore) Append(ctx context.Context, batch Batch) error {
	if err := p.store.Append(ctx, batch); err != nil {
		return err
	}
	if batch.After == 0 {
		close(p.accepted)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestCloseJoinsAnAcceptedStartBeforeLocalPublication(t *testing.T) {
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	store := publicationStore{store: NewMemoryStore(), accepted: make(chan struct{}), release: release}
	admitted := make(chan context.Context, 1)
	runtime := kernelRuntime(t, Config{Store: store, ProcessAdmitter: ProcessAdmitterFunc(func(ctx context.Context, _ ProcessAdmission) error {
		admitted <- ctx
		return nil
	})})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	id := foregroundStartID()
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := runtime.Start(ctx, program, StartRequest[int]{ID: id})
		returned <- err
	}()
	select {
	case <-store.accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	closeContext, stopWaiting := context.WithCancel(ctx)
	stopWaiting()
	if err := runtime.Close(closeContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close could not leave a bounded wait: %v", err)
	}
	admission := <-admitted
	select {
	case <-admission.Done():
	case <-ctx.Done():
		t.Fatal("Close did not cancel pending publication")
	}
	close(release)
	if err := <-returned; !errors.Is(err, ErrClosed) {
		t.Fatalf("a new local writer appeared after Close: %v", err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	log, _, err := store.Load(ctx, id.Root())
	if err != nil || log.Head() != 2 || log.Epoch() != 1 {
		t.Fatalf("closing changed the accepted start: head=%d epoch=%d error=%v", log.Head(), log.Epoch(), err)
	}
	if _, found := runtime.Process(id.Root()); found {
		t.Fatal("closed Runtime retained an unpublished writer")
	}
	replacement := kernelRuntime(t, Config{Store: store.store})
	recovered := controlValue(replacement.Recover(ctx, program, id.Root()))
	if result := controlValue(recovered.Await(ctx)); controlValue(result.Output()) != 0 {
		t.Fatal("accepted start could not recover after local publication was canceled")
	}
}

func TestRecoveryActivationFailureDoesNotPublishAWriter(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		name := "before append"
		if acknowledged {
			name = "lost acknowledgment"
		}
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			original := kernelRuntime(t, Config{Store: store})
			program := lifecycleEchoProgram(t, original, "test.activation")
			handle := controlValue(original.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			waitForKernelStep(t, ctx, handle, 1)
			if err := original.Close(ctx); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("activation acknowledgment interrupted")
			fault := &lifecycleFaultStore{store: store, kind: RecordKindActivated, cause: cause, commitFirst: acknowledged}
			replacement := kernelRuntime(t, Config{Store: fault})
			if recovered, err := replacement.Recover(ctx, program, handle.ID()); !errors.Is(err, cause) || recovered != nil {
				t.Fatalf("unacknowledged activation published a handle: %v", err)
			}
			if _, found := replacement.Process(handle.ID()); found {
				t.Fatal("unacknowledged activation acquired local ownership")
			}
			log, _, err := store.Load(ctx, handle.ID())
			if err != nil {
				t.Fatal(err)
			}
			wantEpoch := Epoch(1)
			if acknowledged {
				wantEpoch++
			}
			if log.Epoch() != wantEpoch {
				t.Fatalf("activation failure changed the durable epoch: %d", log.Epoch())
			}
			recovered := controlValue(replacement.Recover(ctx, program, handle.ID()))
			if recovered.Epoch() != wantEpoch+1 {
				t.Fatal("retry reused an ambiguous writer epoch")
			}
			if err := recovered.Answer(ctx, kernelWait().Key, 9); err != nil {
				t.Fatal(err)
			}
			if result := controlValue(recovered.Await(ctx)); controlValue(result.Output()) != 9 {
				t.Fatal("retry could not resume the accepted computation")
			}
		})
	}
}

func TestOpaqueCheckpointRejectionCannotAdvanceTheEpoch(t *testing.T) {
	store := NewMemoryStore()
	original := kernelRuntime(t, Config{Store: store, CheckpointInterval: 1})
	program := lifecycleEchoProgram(t, original, "test.checkpoint_owner")
	handle := controlValue(original.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if _, checkpoint := entry.Record.(Checkpoint); checkpoint {
			break
		}
	}
	if err := original.Close(ctx); err != nil {
		t.Fatal(err)
	}
	log, err := original.History(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	records := make([]Record, len(log.Entries))
	for index, entry := range log.Entries {
		records[index] = entry.Record
		if checkpoint, ok := entry.Record.(Checkpoint); ok {
			checkpoint.Processes[0].State = controlValue(EncodeExecutionState("test.foreign_state", 0))
			records[index] = checkpoint
		}
	}
	corrupted := NewMemoryStore()
	if appendErr := corrupted.Append(ctx, Batch{Root: log.Root, Records: records}); appendErr != nil {
		t.Fatal(appendErr)
	}
	replacement := kernelRuntime(t, Config{Store: corrupted})
	if _, recoveryErr := replacement.Recover(ctx, program, log.Root); !errors.Is(recoveryErr, ErrInvalidExecutionState) {
		t.Fatalf("checkpoint bypassed its opaque-state owner: %v", recoveryErr)
	}
	retained, _, err := corrupted.Load(ctx, log.Root)
	if err != nil || retained.Head() != log.Head() || retained.Epoch() != log.Epoch() {
		t.Fatal("failed restoration acquired a writer or changed history")
	}
}
