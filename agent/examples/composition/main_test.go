package main

import (
	startrand "crypto/rand"

	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "embedded: EMBEDDED\ncomposed: COMPOSITION | model: composition\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestRunHonorsCancellationBeforeExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var output bytes.Buffer
	if err := run(ctx, &output); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context cancellation", err)
	}
	if output.Len() != 0 {
		t.Fatalf("canceled run published output: %q", output.String())
	}
}

func TestUppercaseExecutionRejectsInvalidAdvancement(t *testing.T) {
	deployment := compositionValue(newUppercaseDeployment())
	execution := compositionValue(deployment.Definition().Start(compositionValue(agentrun.EncodePayload(textInput{Text: "once"}))))
	before := compositionValue(execution.Snapshot())
	if _, err := execution.Step(t.Context(), []agentrun.Signal{{}}); !errors.Is(err, agentrun.ErrInvalidSignal) {
		t.Fatalf("unexpected Signal error = %v", err)
	}
	if after := compositionValue(execution.Snapshot()); !bytes.Equal(after.Payload(), before.Payload()) {
		t.Fatal("rejected input advanced the execution")
	}
	transition := compositionValue(execution.Step(t.Context(), nil))
	if transition.Kind() != agentrun.TransitionKindComplete {
		t.Fatalf("valid step kind = %s, want complete", transition.Kind())
	}
	completed := compositionValue(execution.Snapshot())
	if _, err := execution.Step(t.Context(), nil); err == nil {
		t.Fatal("completed execution accepted another step")
	}
	if after := compositionValue(execution.Snapshot()); !bytes.Equal(after.Payload(), completed.Payload()) {
		t.Fatal("repeated completion changed the execution")
	}
}

func TestUppercaseRestoreHonorsCancellation(t *testing.T) {
	deployment, err := newUppercaseDeployment()
	if err != nil {
		t.Fatal(err)
	}
	state, err := agentrun.ParseExecutionState(uppercaseStateKind, []byte(`{"text":"x","done":false}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := deployment.Definition().Restore(ctx, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore error=%v", err)
	}
}

type compositionFixture struct {
	local       agentrun.Program[agentrun.Payload, agentrun.Payload]
	model       agentrun.Program[agent.Input, agent.Output]
	composition agentrun.Program[agentrun.Payload, agentrun.Payload]
}

func newCompositionFixture(t *testing.T, model agentrun.Program[agent.Input, agent.Output]) compositionFixture {
	t.Helper()
	local, err := newUppercaseDeployment()
	if err != nil {
		t.Fatal(err)
	}
	if !model.Valid() {
		if model, err = newModelDeployment(); err != nil {
			t.Fatal(err)
		}
	}
	composition, err := newCompositionDeployment(local, model)
	if err != nil {
		t.Fatal(err)
	}
	return compositionFixture{local: local, model: model, composition: composition}
}

func newCompositionEngine(t *testing.T, config agentrun.Config) *agentrun.Runtime {
	t.Helper()
	engine, err := agentrun.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return engine
}

func TestDefinitionsRejectInvalidBoundaryValues(t *testing.T) {
	if _, err := newCompositionDeployment(agentrun.Program[agentrun.Payload, agentrun.Payload]{}, agentrun.Program[agent.Input, agent.Output]{}); !errors.Is(err, agentrun.ErrInvalidProgram) {
		t.Fatalf("invalid child bindings: %v", err)
	}
	fixture := newCompositionFixture(t, agentrun.Program[agent.Input, agent.Output]{})
	for _, test := range []struct {
		name  string
		local agentrun.Program[agentrun.Payload, agentrun.Payload]
		model agentrun.Program[agent.Input, agent.Output]
	}{
		{name: "local child has Interaction schema", local: fixture.model.Erase(), model: fixture.model},
	} {
		t.Run(test.name, func(t *testing.T) {
			deployment, err := newCompositionDeployment(test.local, test.model)
			if deployment.Valid() || !errors.Is(err, workflow.ErrInvalidDefinitionConfig) {
				t.Fatalf("incompatible child schemas produced deployment=%v error=%v", deployment.Valid(), err)
			}
		})
	}
	for _, test := range []struct {
		deployment agentrun.Program[agentrun.Payload, agentrun.Payload]
		input      agentrun.Payload
	}{
		{deployment: fixture.local, input: compositionValue(agentrun.EncodePayload(textInput{Text: "x"}))},
		{deployment: fixture.composition, input: compositionValue(agentrun.EncodePayload(compositionInput{Prompt: "x"}))},
	} {
		t.Run(test.deployment.Descriptor().Name(), func(t *testing.T) {
			input := compositionValue(agentrun.ParsePayload([]byte(`{}`)))
			if _, startErr := test.deployment.Definition().Start(input); startErr == nil {
				t.Error("Start accepted missing required input")
			}
			execution := compositionValue(test.deployment.Definition().Start(test.input))
			state := compositionValue(execution.Snapshot())
			payload := append(bytes.TrimSuffix(state.Payload(), []byte("}")), []byte(`,"unknown":true}`)...)
			invalid := compositionValue(agentrun.ParseExecutionState(state.Kind(), payload))
			if _, err := test.deployment.Definition().Restore(t.Context(), invalid); err == nil {
				t.Error("Restore accepted unknown state fields")
			}
		})
	}
}

// The capture must hold the whole tree at rest: a sibling that finishes after
// it would advance the authoritative head and fence the restoration.
func TestUnknownChildSettlementSurvivesCompositionRecovery(t *testing.T) {
	synctest.Test(t, testUnknownChildSettlementSurvivesCompositionRecovery)
}

func testUnknownChildSettlementSurvivesCompositionRecovery(t *testing.T) {
	store := agentrun.NewMemoryStore()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	model, dispatcher := newUncertainModelDeployment(t)
	fixture := newCompositionFixture(t, model)
	observations := &runtest.ObservationRecorder{}
	engine := newCompositionEngine(t, agentrun.Config{Store: store, EventListeners: []agentrun.EventListener{observations}})
	input, err := fixture.composition.Descriptor().EncodeInput(compositionInput{Prompt: "recovered"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(ctx, fixture.composition, agentrun.StartRequest[agentrun.Payload]{ID: agentrun.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { retireWriter(t, root) })
	unknownEvent, err := observations.AwaitEvent(ctx, func(event agentrun.Event) bool {
		fact, ok := event.EffectFinished()
		return ok && fact.SettlementStatus() == agentrun.SettlementStatusUnknown
	})
	if err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	inspection, err := engine.InspectTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if report, found := inspection.Process(root.Relation().ProcessID()); !found || report.Snapshot.Status() != agentrun.StatusWaiting {
		t.Fatal("composition is not waiting on its unknown child")
	}
	tree, err := engine.CaptureTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != 5 {
		t.Fatalf("captured Processes=%d, want the root, two adapters, and two leaves", len(tree.ProcessSnapshots()))
	}

	restoredEngine := newCompositionEngine(t, agentrun.Config{Store: store})
	restored, err := restoredEngine.Recover(ctx, fixture.composition, tree)
	if err != nil {
		t.Fatal(err)
	}
	child := assertRestoredUnknownChild(ctx, t, restoredEngine, restored, unknownEvent, dispatcher)
	// The external boundary supplies the recovered result; the Host never decodes
	// the Interaction payload or edits either Strategy's execution state.
	var settlement agentrun.Settlement
	select {
	case settlement = <-dispatcher.settlements:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	effectID, _ := unknownEvent.EffectID()
	if resolveErr := child.ResolveUnknownEffect(ctx, effectID, settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	result, err := restored.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	output, err := decodeCompleted[compositionOutput](result)
	if err != nil || output != (compositionOutput{Local: "RECOVERED", Model: "model: recovered"}) {
		t.Fatalf("output=%+v error=%v", output, err)
	}
	assertRecoveredTreeCompleted(ctx, t, restoredEngine, restored.Relation().ProcessID(), tree, dispatcher)
	if releaseErr := restoredEngine.ReleaseTree(ctx, restored.Relation().ProcessID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
}

// retireWriter stops the original root after restoration fenced it, so its
// only acceptable outcomes are completion or an incarnation conflict.
func retireWriter(t *testing.T, root *agentrun.Handle[agentrun.Payload]) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	if err := root.Kill(ctx, "release retired writer"); err != nil &&
		!errors.Is(err, agentrun.ErrProcessFinished) && !errors.Is(err, agentrun.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if err := root.Join(ctx); err != nil && !errors.Is(err, agentrun.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
}

func assertRestoredUnknownChild(
	ctx context.Context,
	t *testing.T,
	engine *agentrun.Runtime,
	restored *agentrun.Handle[agentrun.Payload],
	unknownEvent agentrun.Event,
	dispatcher *lostResponseDispatcher,
) *agentrun.Handle[agentrun.Payload] {
	t.Helper()
	child, found := engine.Process(unknownEvent.Relation().ProcessID())
	if !found {
		t.Fatal("restoration lost the model child identity")
	}
	inspection, err := engine.InspectTree(ctx, restored.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	childReport, childFound := inspection.Process(child.Relation().ProcessID())
	rootReport, rootFound := inspection.Process(restored.Relation().ProcessID())
	unknown := childReport.Snapshot.UnknownEffectIDs()
	effectID, present := unknownEvent.EffectID()
	if !childFound || !present || len(unknown) != 1 || unknown[0] != effectID {
		t.Fatalf("unknown Effects=%v original=%s", unknown, effectID)
	}
	if !rootFound || rootReport.Snapshot.Status() != agentrun.StatusWaiting || dispatcher.calls.Load() != 1 {
		t.Fatalf("root status=%s dispatches=%d", rootReport.Snapshot.Status(), dispatcher.calls.Load())
	}
	return child
}

func assertRecoveredTreeCompleted(
	ctx context.Context,
	t *testing.T,
	engine *agentrun.Runtime,
	rootID agentrun.ProcessID,
	original agentrun.TreeSnapshot,
	dispatcher *lostResponseDispatcher,
) {
	t.Helper()
	final, err := engine.CaptureTree(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.ProcessSnapshots()) != len(original.ProcessSnapshots()) || dispatcher.calls.Load() != 1 {
		t.Fatalf("final Processes=%d dispatches=%d", len(final.ProcessSnapshots()), dispatcher.calls.Load())
	}
	inspection, err := engine.InspectTree(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range original.ProcessSnapshots() {
		report, found := inspection.Process(process.Relation().ProcessID())
		if !found || report.Snapshot.Status() != agentrun.StatusCompleted {
			t.Fatalf("original Process %s was lost or did not complete", process.Relation().ProcessID())
		}
	}
}

func newUncertainModelDeployment(t *testing.T) (agentrun.Program[agent.Input, agent.Output], *lostResponseDispatcher) {
	t.Helper()
	client, err := chatclient.New(compositionModel{}, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := agent.NewDefinition(agent.DefinitionConfig{
		Name: "example.uncertain_model", Description: "Recover a lost composition response.", MaxModelCalls: agentrun.NewQuota(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := agent.NewDispatcher(definition, agent.DispatcherConfig{Model: client})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &lostResponseDispatcher{next: next, settlements: make(chan agentrun.Settlement, 1)}
	deployment, err := agentrun.Bind[agent.Input, agent.Output](agentrun.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agentrun.ComputeDigest([]byte("uncertain-composition-implementation")),
		ConfigurationDigest:  agentrun.ComputeDigest([]byte("uncertain-composition-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment, dispatcher
}

type lostResponseDispatcher struct {
	next        agentrun.Dispatcher
	settlements chan agentrun.Settlement
	calls       atomic.Int64
}

func (l *lostResponseDispatcher) Dispatch(ctx context.Context, request agentrun.EffectRequest, emit agentrun.DeltaEmitter) (agentrun.Settlement, error) {
	l.calls.Add(1)
	settlement, err := l.next.Dispatch(ctx, request, emit)
	if err != nil {
		return agentrun.Settlement{}, err
	}
	select {
	case l.settlements <- settlement:
		return agentrun.Settlement{}, errors.New("response lost after external completion")
	case <-ctx.Done():
		return agentrun.Settlement{}, ctx.Err()
	}
}

func (*lostResponseDispatcher) Policy(agentrun.Effect) agentrun.EffectPolicy {
	return agentrun.EffectPolicy{Replay: agentrun.ReplayPolicyNever}
}

func TestCompositionRestoresEverySignalBoundary(t *testing.T) {
	fixture := newCompositionFixture(t, agentrun.Program[agent.Input, agent.Output]{})
	base := fixture.composition
	definition := &recordingDefinition{Definition: base.Definition()}
	deployment, err := agentrun.Bind[agentrun.Payload, agentrun.Payload](agentrun.BindingConfig{
		Definition:           definition,
		ImplementationDigest: base.ProgramRef().ImplementationDigest(),
		ConfigurationDigest:  base.ProgramRef().ConfigurationDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := newCompositionEngine(t, agentrun.Config{Store: agentrun.NewMemoryStore()})
	input, err := base.Descriptor().EncodeInput(compositionInput{Prompt: "boundaries"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, deployment, input)
	if err != nil || result.Termination().Status() != agentrun.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Termination().Status(), err)
	}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{
		Definition: base.Definition(), Input: input, RestoredCases: definition.samples,
	})
	for _, sample := range definition.samples {
		t.Run(sample.Name+" rejects unexpected first signal", func(t *testing.T) {
			execution := restoreComposition(t, base, sample)
			if _, err := execution.Step(t.Context(), append([]agentrun.Signal{{}}, sample.Signals...)); err == nil {
				t.Fatal("unexpected first Signal was discarded")
			}
		})
	}
	opening, completion := childWaitSamples(t, definition.samples)
	for _, sample := range []runtest.ExecutionConformanceCase{opening, completion} {
		t.Run(sample.Name+" consumes only its prefix", func(t *testing.T) {
			transition, err := restoreComposition(t, base, sample).Step(t.Context(), append(sample.Signals[:1:1], completion.Signals[0]))
			if err != nil || transition.ConsumedSignals() != 1 {
				t.Fatalf("consumed=%d error=%v", transition.ConsumedSignals(), err)
			}
			if sample.Name == opening.Name && transition.Kind() != agentrun.TransitionKindWait {
				t.Fatalf("kind=%s, want wait", transition.Kind())
			}
		})
	}
	// An opening acknowledges whichever WaitID the Runtime minted; only the
	// completion can answer another wait.
	t.Run(completion.Name+" rejects unrelated wait", func(t *testing.T) {
		if _, err := restoreComposition(t, base, completion).Step(t.Context(), []agentrun.Signal{unrelatedWaitSignal(t, completion.Signals[0])}); err == nil {
			t.Fatal("unrelated wait was accepted")
		}
	})
	if err := engine.ReleaseTree(t.Context(), result.ProcessID()); err != nil {
		t.Fatal(err)
	}
}

func restoreComposition(t *testing.T, deployment agentrun.Program[agentrun.Payload, agentrun.Payload], sample runtest.ExecutionConformanceCase) agentrun.Execution {
	t.Helper()
	execution, err := deployment.Definition().Restore(t.Context(), sample.State)
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func childWaitSamples(t *testing.T, samples []runtest.ExecutionConformanceCase) (opening, completion runtest.ExecutionConformanceCase) {
	t.Helper()
	for _, sample := range samples {
		if len(sample.Signals) == 0 {
			continue
		}
		if _, err := sample.Signals[0].Frame[agentrun.ChildWaitOpened](); err == nil {
			opening = sample
		}
		if _, err := sample.Signals[0].Frame[agentrun.ChildWaitSatisfied](); err == nil {
			completion = sample
		}
	}
	if !opening.State.Valid() || !completion.State.Valid() {
		t.Fatal("execution did not visit both child wait boundaries")
	}
	return opening, completion
}

func unrelatedWaitSignal(t *testing.T, signal agentrun.Signal) agentrun.Signal {
	t.Helper()
	encoded, err := jsonv2.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	waitID, _ := signal.WaitID()
	encoded = bytes.ReplaceAll(encoded, []byte(`"`+waitID.String()+`"`), []byte(`"wait:unrelated"`))
	var unrelated agentrun.Signal
	if err := jsonv2.Unmarshal(encoded, &unrelated); err != nil {
		t.Fatal(err)
	}
	return unrelated
}

type recordingDefinition struct {
	agentrun.Definition
	samples []runtest.ExecutionConformanceCase
}

func (r *recordingDefinition) Start(input agentrun.Payload) (agentrun.Execution, error) {
	execution, err := r.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{Execution: execution, definition: r}, nil
}

func (r *recordingDefinition) Restore(ctx context.Context, state agentrun.ExecutionState) (agentrun.Execution, error) {
	execution, err := r.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{Execution: execution, definition: r}, nil
}

type recordingExecution struct {
	agentrun.Execution
	definition *recordingDefinition
}

func (r *recordingExecution) Step(ctx context.Context, signals []agentrun.Signal) (agentrun.Transition, error) {
	state, err := r.Snapshot()
	if err != nil {
		return agentrun.Transition{}, err
	}
	r.definition.samples = append(r.definition.samples, runtest.ExecutionConformanceCase{
		Name: fmt.Sprintf("step %d", len(r.definition.samples)+1), State: state, Signals: slices.Clone(signals),
	})
	return r.Execution.Step(ctx, signals)
}

func TestCompositionPreservesChildFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		refuse   bool
		wantCode string
	}{
		{name: "start failure", refuse: true, wantCode: "engine.child.admission.rejected"},
		{name: "step failure", wantCode: "example.injected_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompositionFixture(t, newFailingModelDeployment(t))
			config := agentrun.Config{Store: agentrun.NewMemoryStore()}
			if test.refuse {
				refused := fixture.model.ProgramRef()
				config.ProcessAdmitter = agentrun.ProcessAdmitterFunc(func(_ context.Context, admission agentrun.ProcessAdmission) error {
					if admission.ProgramRef() == refused {
						return errors.New("model child refused")
					}
					return nil
				})
			}
			engine := newCompositionEngine(t, config)
			input, err := fixture.composition.Descriptor().EncodeInput(compositionInput{Prompt: "failure"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runtest.Observe(t.Context(), engine, fixture.composition, input)
			if err != nil || result.Termination().Status() != agentrun.StatusFailed {
				t.Fatalf("status=%s error=%v", result.Termination().Status(), err)
			}
			if failure, found := result.Termination().Failure(); !found || failure.Code() != test.wantCode {
				t.Fatalf("failure=%s, want %s", failure.Code(), test.wantCode)
			}
			if decoded, err := decodeCompleted[compositionOutput](result); err == nil || decoded != (compositionOutput{}) {
				t.Fatalf("failed child became composition output: output=%+v error=%v", decoded, err)
			}
			if err := engine.ReleaseTree(t.Context(), result.ProcessID()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func newFailingModelDeployment(t *testing.T) agentrun.Program[agent.Input, agent.Output] {
	t.Helper()
	model, err := newModelDeployment()
	if err != nil {
		t.Fatal(err)
	}
	failing, err := agentrun.Bind[agent.Input, agent.Output](agentrun.BindingConfig{
		Definition:           failingDefinition{Definition: model.Definition()},
		ImplementationDigest: agentrun.ComputeDigest([]byte("failing-composition-child")),
		ConfigurationDigest:  model.ProgramRef().ConfigurationDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return failing
}

type failingDefinition struct{ agentrun.Definition }

func (f failingDefinition) Start(input agentrun.Payload) (agentrun.Execution, error) {
	execution, err := f.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return failingExecution{Execution: execution}, nil
}

func (f failingDefinition) Restore(ctx context.Context, state agentrun.ExecutionState) (agentrun.Execution, error) {
	execution, err := f.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return failingExecution{Execution: execution}, nil
}

type failingExecution struct{ agentrun.Execution }

func (f failingExecution) Step(context.Context, []agentrun.Signal) (agentrun.Transition, error) {
	failure, err := agentrun.NewFailure(agentrun.FailureKindExecution, "example.injected_failure", "composition child failed")
	if err != nil {
		return agentrun.Transition{}, err
	}
	return agentrun.Fail(0, failure)
}

func TestCompositionRejectsUnaddressedInputAtAdmission(t *testing.T) {
	fixture := newCompositionFixture(t, agentrun.Program[agent.Input, agent.Output]{})
	base := fixture.composition
	input, err := base.Descriptor().EncodeInput(compositionInput{Prompt: "admission"})
	if err != nil {
		t.Fatal(err)
	}
	conformancetest.Run(t, agentrun.BindingConfig{
		Definition:           base.Definition(),
		ImplementationDigest: base.ProgramRef().ImplementationDigest(),
		ConfigurationDigest:  base.ProgramRef().ConfigurationDigest(),
	}, agentrun.Config{Store: agentrun.NewMemoryStore()}, input)
}

func compositionValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func TestCompositionRejectsMissingModelInputWithoutPublishingOutput(t *testing.T) {
	r, err := (compositionModel{}).Call(t.Context(), &chat.Request{})
	if r != nil || err == nil {
		t.Fatalf("empty input produced %v %v", r, err)
	}
}
