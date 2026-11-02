package run

import (
	"context"
	"crypto/rand"
	"strconv"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
)

const kernelTestBuild = "kernel-contracts"
const kernelTestTimeout = 5 * time.Second

func kernelRuntime(t testing.TB, config Config) *Runtime {
	t.Helper()
	if config.Store == nil {
		config.Store = NewMemoryStore()
	}
	if config.Build == "" {
		config.Build = kernelTestBuild
	}
	runtime, err := New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), kernelTestTimeout)
		defer cancel()
		if closeErr := runtime.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return runtime
}

func kernelDescriptor(t testing.TB, name string) Descriptor {
	t.Helper()
	schema := controlValue(jsonschema.For[int]())
	return controlValue(NewDescriptor(DescriptorConfig{Name: name, Description: "Exercise durable kernel contracts.", InputSchema: schema, OutputSchema: schema, SignalSchema: schema}))
}

func kernelWait() Wait {
	return Wait{Key: controlValue(ParseWaitKey("input")), Question: controlValue(EncodePayload("Continue?")), AnswerSchema: controlValue(jsonschema.For[int]()), MaxAnswerBytes: 128}
}

type treeFixtureDefinition struct {
	descriptor Descriptor
	children   []Program[Payload, Payload]
	budget     Budget
	hold       bool
}

func (t *treeFixtureDefinition) Descriptor() Descriptor { return t.descriptor }
func (t *treeFixtureDefinition) ChildPrograms() []Program[Payload, Payload] {
	return t.children
}
func (t *treeFixtureDefinition) Start(Payload) (Execution, error) {
	return &treeFixtureExecution{definition: t}, nil
}
func (t *treeFixtureDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	value, err := state.Decode[treeFixtureState]("test.tree")
	if err != nil {
		return nil, err
	}
	return &treeFixtureExecution{definition: t, state: value}, nil
}

type treeFixtureState struct {
	Spawned bool
	Exits   int
}

type treeFixtureExecution struct {
	definition *treeFixtureDefinition
	state      treeFixtureState
}

func (t *treeFixtureExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("test.tree", t.state)
}
func (t *treeFixtureExecution) Step(_ context.Context, turn Turn) (Transition, error) {
	consumed := uint32(len(turn.Messages))
	if !t.state.Spawned && len(t.definition.children) > 0 {
		t.state.Spawned = true
		effects := make([]Effect, len(t.definition.children))
		for index, child := range t.definition.children {
			effects[index] = controlValue(NewChildStartEffect(ChildSpec{Key: controlValue(ParseChildKey("child-" + strconv.Itoa(index))), ProgramRef: child.ProgramRef(), Input: controlValue(EncodePayload(0)), Budget: t.definition.budget}))
		}
		return Yield(consumed, effects)
	}
	for _, message := range turn.Messages {
		if _, ok := message.(Exit); ok {
			t.state.Exits++
		}
	}
	if t.state.Exits < len(t.definition.children) {
		return Yield(consumed, nil)
	}
	if t.definition.hold {
		return Yield(consumed, nil, kernelWait())
	}
	return Complete(consumed, controlValue(EncodePayload(t.state.Exits)))
}

func treeFixtureProgram(t testing.TB, runtime *Runtime, count int, holdRoot, holdChildren bool, budget Budget) Program[int, int] {
	t.Helper()
	child := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.leaf"), hold: holdChildren}, Revision: "leaf", Configuration: holdChildren}))
	children := make([]Program[Payload, Payload], count)
	for index := range children {
		children[index] = child.Erase()
	}
	return controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.parent"), children: children, budget: budget, hold: holdRoot}, Revision: "parent", Configuration: struct {
		Count  int
		Hold   bool
		Budget Budget
	}{count, holdRoot, budget}}))
}

type boundaryDefinition struct {
	descriptor Descriptor
	step       func(context.Context, Turn) (Transition, error)
}

func (b *boundaryDefinition) Descriptor() Descriptor                   { return b.descriptor }
func (*boundaryDefinition) ChildPrograms() []Program[Payload, Payload] { return nil }
func (b *boundaryDefinition) Start(Payload) (Execution, error) {
	return &boundaryExecution{step: b.step}, nil
}
func (b *boundaryDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	if _, err := state.Decode[int]("test.boundary"); err != nil {
		return nil, err
	}
	return &boundaryExecution{step: b.step}, nil
}

type boundaryExecution struct {
	step func(context.Context, Turn) (Transition, error)
}

func (b *boundaryExecution) Step(ctx context.Context, turn Turn) (Transition, error) {
	return b.step(ctx, turn)
}
func (*boundaryExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("test.boundary", 0)
}

type boundaryDispatcher struct {
	policy   EffectPolicy
	dispatch func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error)
}

func (b boundaryDispatcher) Policy(Effect) EffectPolicy { return b.policy }
func (b boundaryDispatcher) Dispatch(ctx context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
	return b.dispatch(ctx, request, emit)
}

// hostStartID gives each test start a fresh identity in a Host-owned scope.
func hostStartID() StartID { return StartID{Scope: "test", Key: rand.Text()} }
