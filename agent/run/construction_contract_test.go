package run

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestInvalidBindingsCannotStartDurableWork(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	definition := &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.binding")}
	var nilDefinition *treeFixtureDefinition
	var nilDispatcher *boundaryDispatcher
	for _, test := range []struct {
		name     string
		compiler *Compiler
		config   BindingConfig
	}{
		{"missing compiler", nil, BindingConfig{Definition: definition, Revision: "one"}},
		{"missing definition", runtime.Compiler(), BindingConfig{Revision: "one"}},
		{"typed nil definition", runtime.Compiler(), BindingConfig{Definition: nilDefinition, Revision: "one"}},
		{"typed nil dispatcher", runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: nilDispatcher, Revision: "one"}},
		{"unserializable configuration", runtime.Compiler(), BindingConfig{Definition: definition, Configuration: func() {}, Revision: "one"}},
		{"invalid revision", runtime.Compiler(), BindingConfig{Definition: definition, Revision: " padded "}},
		{"invalid descriptor", runtime.Compiler(), BindingConfig{Definition: &treeFixtureDefinition{}, Revision: "one"}},
		{"invalid child", runtime.Compiler(), BindingConfig{Definition: &treeFixtureDefinition{descriptor: definition.descriptor, children: []Program[Payload, Payload]{{}}}, Revision: "one"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if program, err := Bind[int, int](test.compiler, test.config); err == nil || program.Valid() {
				t.Fatalf("invalid declaration accepted: valid=%t error=%v", program.Valid(), err)
			}
		})
	}
	if _, err := Bind[string, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}); !errors.Is(err, ErrInvalidProgram) {
		t.Fatalf("mismatched input port: %v", err)
	}
	if _, err := Bind[int, string](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}); !errors.Is(err, ErrInvalidProgram) {
		t.Fatalf("mismatched output port: %v", err)
	}
	for _, change := range []string{"contract", "children"} {
		t.Run(change+" after binding", func(t *testing.T) {
			owned := &treeFixtureDefinition{descriptor: definition.descriptor}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: owned, Revision: "one"}))
			if change == "contract" {
				owned.descriptor = kernelDescriptor(t, "test.changed")
			} else {
				owned.children = []Program[Payload, Payload]{treeFixtureProgram(t, runtime, 0, false, false, Budget{}).Erase()}
			}
			if _, err := runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}); !errors.Is(err, ErrInvalidProgram) {
				t.Fatalf("mutable binding escaped its reference: %v", err)
			}
		})
	}
}

func TestInitializationDefectsNeverPublishStarted(t *testing.T) {
	for _, mode := range []initializationDefect{startReturnsNil, startPanics, snapshotInvalid, snapshotPanics, restoreReturnsNil, restoreFails, restorePanics} {
		t.Run(string(mode), func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store})
			definition := &defectiveDefinition{descriptor: kernelDescriptor(t, "test.initialization"), mode: mode}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
			id := hostStartID()
			if handle, err := runtime.Start(t.Context(), program, StartRequest[int]{ID: id}); err == nil || handle != nil {
				t.Fatalf("defective initialization admitted: handle=%v error=%v", handle, err)
			}
			if _, found, err := store.Load(t.Context(), id.Root()); err != nil || found {
				t.Fatalf("initialization defect published records: found=%t error=%v", found, err)
			}
		})
	}
}

type initializationDefect string

const (
	startReturnsNil   initializationDefect = "start returns nil"
	startPanics       initializationDefect = "start panics"
	snapshotInvalid   initializationDefect = "snapshot invalid"
	snapshotPanics    initializationDefect = "snapshot panics"
	restoreReturnsNil initializationDefect = "restore returns nil"
	restoreFails      initializationDefect = "restore fails"
	restorePanics     initializationDefect = "restore panics"
)

type defectiveDefinition struct {
	descriptor Descriptor
	mode       initializationDefect
}

func (d *defectiveDefinition) Descriptor() Descriptor                   { return d.descriptor }
func (*defectiveDefinition) ChildPrograms() []Program[Payload, Payload] { return nil }

func (d *defectiveDefinition) Start(Payload) (Execution, error) {
	switch d.mode {
	case startReturnsNil:
		return nil, nil
	case startPanics:
		panic("start defect")
	default:
		return &defectiveExecution{mode: d.mode}, nil
	}
}

func (d *defectiveDefinition) Restore(context.Context, ExecutionState) (Execution, error) {
	switch d.mode {
	case restoreReturnsNil:
		return nil, nil
	case restoreFails:
		return nil, ErrInvalidExecutionState
	case restorePanics:
		panic("restore defect")
	default:
		return &defectiveExecution{mode: d.mode}, nil
	}
}

type defectiveExecution struct{ mode initializationDefect }

func (*defectiveExecution) Step(context.Context, Turn) (Transition, error) {
	return Complete(0, controlValue(EncodePayload(0)))
}

func (d *defectiveExecution) Snapshot() (ExecutionState, error) {
	switch d.mode {
	case snapshotInvalid:
		return ExecutionState{}, nil
	case snapshotPanics:
		panic("snapshot defect")
	default:
		return EncodeExecutionState("test.defective", 0)
	}
}

func TestRuntimeConfigurationRejectsUnusableBoundaries(t *testing.T) {
	var nilStore *MemoryStore
	var nilAdmitter ProcessAdmitterFunc
	for _, config := range []Config{
		{}, {Store: nilStore, Build: kernelTestBuild},
		{Store: NewMemoryStore()},
		{Store: NewMemoryStore(), Build: " padded "},
		{Store: NewMemoryStore(), Build: kernelTestBuild, ProcessAdmitter: nilAdmitter},
		{Store: NewMemoryStore(), Build: kernelTestBuild, StoreTimeout: -time.Second},
	} {
		if _, err := New(t.Context(), config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("unusable config admitted: %+v error=%v", config, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(ctx, Config{Build: kernelTestBuild, Store: NewMemoryStore()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled construction: %v", err)
	}
	var missing *Runtime
	if missing.Compiler() != nil {
		t.Fatal("nil Runtime granted compilation authority")
	}
	if _, err := missing.Start(t.Context(), Program[int, int]{}, StartRequest[int]{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := missing.Run(t.Context(), Program[int, int]{}, 0); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := missing.Recover(t.Context(), Program[int, int]{}, ProcessID{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := missing.Inspect(t.Context(), ProcessID{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if err := missing.Close(t.Context()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if err := missing.ReleaseTree(t.Context(), ProcessID{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, found := missing.Process(ProcessID{}); found {
		t.Fatal("nil Runtime routed a Process")
	}
}

func TestRejectedRootRequestsDoNotAcquireExecutionAuthority(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	if handle, err := runtime.Start(t.Context(), program, StartRequest[int]{}); !errors.Is(err, ErrInvalidStart) || handle != nil {
		t.Fatalf("absent start identity acquired a writer: %v", err)
	}
	floating := controlValue(Bind[float64, float64](runtime.Compiler(), BindingConfig{Definition: newTypedFixtureDefinition[float64](t, "test.float"), Revision: "one"}))
	id := hostStartID()
	if handle, err := runtime.Start(t.Context(), floating, StartRequest[float64]{ID: id, Input: math.NaN()}); err == nil || handle != nil {
		t.Fatal("unrepresentable input acquired a writer")
	}
	if _, err := runtime.Run(t.Context(), floating, math.NaN()); err == nil {
		t.Fatal("foreground input bypassed the canonical payload boundary")
	}
	if _, found, err := store.Load(t.Context(), id.Root()); err != nil || found {
		t.Fatalf("rejected start left durable facts: found=%t error=%v", found, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.Recover(ctx, program, id.Root()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery acquired a writer: %v", err)
	}
	if _, err := runtime.Recover(t.Context(), Program[int, int]{}, id.Root()); !errors.Is(err, ErrInvalidProgram) {
		t.Fatalf("unbound program acquired a writer: %v", err)
	}
	child := controlValue(id.Root().Child(controlValue(ParseChildKey("child"))))
	if _, err := runtime.Recover(t.Context(), program, child); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("child path acquired root authority: %v", err)
	}
	if _, err := runtime.Recover(t.Context(), program, id.Root()); !errors.Is(err, ErrTreeNotFound) {
		t.Fatalf("recovery invented absent history: %v", err)
	}
	if _, err := runtime.Inspect(ctx, id.Root()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inspection error=%v", err)
	}
	if err := runtime.ReleaseTree(ctx, id.Root()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled release error=%v", err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(t.Context(), program, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed runtime admitted foreground work: %v", err)
	}
}

func TestRecoveryCannotReplaceTheAcceptedProgram(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	program := treeFixtureProgram(t, runtime, 0, true, false, Budget{})
	id := hostStartID()
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: id}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	before, _, err := store.Load(ctx, id.Root())
	if err != nil {
		t.Fatal(err)
	}
	replacement := kernelRuntime(t, Config{Store: store})
	changed := controlValue(Bind[int, int](replacement.Compiler(), BindingConfig{Definition: &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.replacement")}, Revision: "two"}))
	if _, recoveryErr := replacement.Recover(ctx, changed, id.Root()); !errors.Is(recoveryErr, ErrInvalidProgram) {
		t.Fatalf("recovery changed the program owning historical effects: %v", recoveryErr)
	}
	after, _, err := store.Load(ctx, id.Root())
	if err != nil || after.Head() != before.Head() || after.Epoch() != before.Epoch() {
		t.Fatalf("rejected replacement advanced the log: %v", err)
	}
	failure := errors.New("storage unavailable")
	unavailable := kernelRuntime(t, Config{Store: &publicObservationStore{store: store, load: func(context.Context, ProcessID) (Log, bool, error) { return Log{}, false, failure }}})
	if _, err := unavailable.Recover(ctx, program, id.Root()); !errors.Is(err, failure) {
		t.Fatalf("recovery discarded its load failure: %v", err)
	}
}

func TestHostStartCannotClaimTheForegroundScope(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := lifecycleEchoProgram(t, runtime, "test.reserved_scope")
	reserved := foregroundStartID()
	if reserved.Valid() || !reserved.Root().Valid() {
		t.Fatal("foreground identity must name a root without being submittable")
	}
	if _, err := runtime.Start(t.Context(), program, StartRequest[int]{ID: reserved}); !errors.Is(err, ErrInvalidStart) {
		t.Fatalf("host start claimed the foreground scope: %v", err)
	}
}
