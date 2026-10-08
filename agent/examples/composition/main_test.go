package main

import (
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

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
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

type compositionFixture struct {
	local       agent.Deployment
	model       agent.Deployment
	composition agent.Deployment
}

func newCompositionFixture(t *testing.T, model agent.Deployment) compositionFixture {
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

func newCompositionEngine(t *testing.T, config agent.EngineConfig) *agent.Engine {
	t.Helper()
	engine, err := agent.NewEngine(config)
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
	if _, err := newCompositionDeployment(agent.Deployment{}, agent.Deployment{}); !errors.Is(err, agent.ErrInvalidDeployment) {
		t.Fatalf("invalid child bindings: %v", err)
	}
	fixture := newCompositionFixture(t, agent.Deployment{})
	for _, deployment := range []agent.Deployment{fixture.local, fixture.composition} {
		t.Run(deployment.Descriptor().Name(), func(t *testing.T) {
			input, err := agent.ParsePayload([]byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, startErr := deployment.Definition().Start(input); startErr == nil {
				t.Error("Start accepted missing required input")
			}
			state, err := agent.ParseExecutionState(deployment.Descriptor().Name(), []byte(`{"phase":"ready","prompt":"x","text":"x","unknown":true}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := deployment.Definition().Restore(t.Context(), state); err == nil {
				t.Error("Restore accepted unknown state fields")
			}
		})
	}
	for _, payload := range []string{
		`{"phase":"unknown","prompt":"x"}`,
		`{"phase":"ready","prompt":"x","child_ids":["child"]}`,
		`{"phase":"waiting_children","prompt":"x"}`,
		`{"phase":"awaiting_child_starts","prompt":"x","child_ids":["same","same"]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			state, err := agent.ParseExecutionState("example.composition", []byte(payload))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.composition.Definition().Restore(t.Context(), state); err == nil {
				t.Fatal("Restore accepted contradictory execution state")
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
	store := agent.NewMemoryTreeCommitter()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	model, dispatcher := newUncertainModelDeployment(t)
	fixture := newCompositionFixture(t, model)
	observations := &agenttest.ObservationRecorder{}
	engine := newCompositionEngine(t, agent.EngineConfig{TreeCommitter: store, EventListeners: []agent.EventListener{observations}})
	input, err := fixture.composition.Descriptor().EncodeInput(compositionInput{Prompt: "recovered"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(ctx, fixture.composition, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { retireWriter(t, root) })
	unknownEvent, err := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		fact, ok := event.EffectFinished()
		return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown
	})
	if err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	inspection, err := engine.InspectTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if report, found := inspection.Process(root.Relation().ProcessID()); !found || report.Snapshot.Status() != agent.StatusWaiting {
		t.Fatal("composition is not waiting on its unknown child")
	}
	tree, err := engine.CaptureTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != 3 {
		t.Fatalf("captured Processes=%d, want one root and two children", len(tree.ProcessSnapshots()))
	}

	restoredEngine := newCompositionEngine(t, agent.EngineConfig{TreeCommitter: store})
	restored, err := restoredEngine.RestoreTree(ctx, fixture.composition, tree)
	if err != nil {
		t.Fatal(err)
	}
	child := assertRestoredUnknownChild(ctx, t, restoredEngine, restored, unknownEvent, dispatcher)
	// The external boundary supplies the recovered result; the Host never decodes
	// the Interaction payload or edits either Strategy's execution state.
	var settlement agent.Settlement
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
func retireWriter(t *testing.T, root *agent.Process) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	if err := root.Kill(ctx, "release retired writer"); err != nil &&
		!errors.Is(err, agent.ErrProcessFinished) && !errors.Is(err, agent.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if err := root.Join(ctx); err != nil && !errors.Is(err, agent.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
}

func assertRestoredUnknownChild(
	ctx context.Context,
	t *testing.T,
	engine *agent.Engine,
	restored *agent.Process,
	unknownEvent agent.Event,
	dispatcher *lostResponseDispatcher,
) *agent.Process {
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
	if !rootFound || rootReport.Snapshot.Status() != agent.StatusWaiting || dispatcher.calls.Load() != 1 {
		t.Fatalf("root status=%s dispatches=%d", rootReport.Snapshot.Status(), dispatcher.calls.Load())
	}
	return child
}

func assertRecoveredTreeCompleted(
	ctx context.Context,
	t *testing.T,
	engine *agent.Engine,
	rootID agent.ProcessID,
	original agent.TreeSnapshot,
	dispatcher *lostResponseDispatcher,
) {
	t.Helper()
	final, err := engine.CaptureTree(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.ProcessSnapshots()) != 3 || dispatcher.calls.Load() != 1 {
		t.Fatalf("final Processes=%d dispatches=%d", len(final.ProcessSnapshots()), dispatcher.calls.Load())
	}
	inspection, err := engine.InspectTree(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range original.ProcessSnapshots() {
		report, found := inspection.Process(process.Relation().ProcessID())
		if !found || report.Snapshot.Status() != agent.StatusCompleted {
			t.Fatalf("original Process %s was lost or did not complete", process.Relation().ProcessID())
		}
	}
}

func newUncertainModelDeployment(t *testing.T) (agent.Deployment, *lostResponseDispatcher) {
	t.Helper()
	client, err := chatclient.New(compositionModel{}, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.uncertain_model", Description: "Recover a lost composition response.", MaxModelCalls: agent.NewQuota(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &lostResponseDispatcher{next: next, settlements: make(chan agent.Settlement, 1)}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("uncertain-composition-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("uncertain-composition-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment, dispatcher
}

type lostResponseDispatcher struct {
	next        agent.Dispatcher
	settlements chan agent.Settlement
	calls       atomic.Int64
}

func (l *lostResponseDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	l.calls.Add(1)
	settlement, err := l.next.Dispatch(ctx, request, emit)
	if err != nil {
		return agent.Settlement{}, err
	}
	select {
	case l.settlements <- settlement:
		return agent.Settlement{}, errors.New("response lost after external completion")
	case <-ctx.Done():
		return agent.Settlement{}, ctx.Err()
	}
}

func (*lostResponseDispatcher) Policy(agent.Effect) agent.EffectPolicy {
	return agent.EffectPolicy{Replay: agent.ReplayPolicyNever}
}

func TestCompositionRestoresEverySignalBoundary(t *testing.T) {
	fixture := newCompositionFixture(t, agent.Deployment{})
	base := fixture.composition
	definition := &recordingDefinition{Definition: base.Definition()}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: base.DeploymentRef().ImplementationDigest(),
		ConfigurationDigest:  base.DeploymentRef().ConfigurationDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := newCompositionEngine(t, agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	input, err := base.Descriptor().EncodeInput(compositionInput{Prompt: "boundaries"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil || result.Termination().Status() != agent.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Termination().Status(), err)
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: base.Definition(), Input: input, RestoredCases: definition.samples,
	})
	for _, sample := range definition.samples {
		t.Run(sample.Name+" rejects unexpected first signal", func(t *testing.T) {
			execution := restoreComposition(t, base, sample)
			if _, err := execution.Step(t.Context(), append([]agent.Signal{{}}, sample.Signals...)); err == nil {
				t.Fatal("unexpected first Signal was discarded")
			}
		})
	}
	opening, completion := childWaitSamples(t, definition.samples)
	for _, sample := range []agenttest.ExecutionConformanceCase{opening, completion} {
		t.Run(sample.Name+" consumes only its prefix", func(t *testing.T) {
			transition, err := restoreComposition(t, base, sample).Step(t.Context(), append(sample.Signals[:1:1], completion.Signals[0]))
			if err != nil || transition.ConsumedSignals() != 1 {
				t.Fatalf("consumed=%d error=%v", transition.ConsumedSignals(), err)
			}
			if sample.Name == opening.Name && transition.Kind() != agent.TransitionKindWait {
				t.Fatalf("kind=%s, want wait", transition.Kind())
			}
		})
	}
	// An opening acknowledges whichever WaitID the Engine minted; only the
	// completion can answer another wait.
	t.Run(completion.Name+" rejects unrelated wait", func(t *testing.T) {
		if _, err := restoreComposition(t, base, completion).Step(t.Context(), []agent.Signal{unrelatedWaitSignal(t, completion.Signals[0])}); err == nil {
			t.Fatal("unrelated wait was accepted")
		}
	})
	if err := engine.ReleaseTree(t.Context(), result.ProcessID()); err != nil {
		t.Fatal(err)
	}
}

func restoreComposition(t *testing.T, deployment agent.Deployment, sample agenttest.ExecutionConformanceCase) agent.Execution {
	t.Helper()
	execution, err := deployment.Definition().Restore(t.Context(), sample.State)
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func childWaitSamples(t *testing.T, samples []agenttest.ExecutionConformanceCase) (opening, completion agenttest.ExecutionConformanceCase) {
	t.Helper()
	for _, sample := range samples {
		var state compositionState
		if err := jsonv2.Unmarshal(sample.State.Payload(), &state); err != nil {
			t.Fatal(err)
		}
		switch state.phase() {
		case compositionAwaitingChildWaitOpen:
			opening = sample
		case compositionWaitingChildren:
			completion = sample
		}
	}
	if !opening.State.Valid() || !completion.State.Valid() {
		t.Fatal("execution did not visit both child wait boundaries")
	}
	return opening, completion
}

func unrelatedWaitSignal(t *testing.T, signal agent.Signal) agent.Signal {
	t.Helper()
	encoded, err := jsonv2.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	waitID, _ := signal.WaitID()
	encoded = bytes.ReplaceAll(encoded, []byte(`"`+waitID.String()+`"`), []byte(`"wait:unrelated"`))
	var unrelated agent.Signal
	if err := jsonv2.Unmarshal(encoded, &unrelated); err != nil {
		t.Fatal(err)
	}
	return unrelated
}

type recordingDefinition struct {
	agent.Definition
	samples []agenttest.ExecutionConformanceCase
}

func (r *recordingDefinition) Start(input agent.Payload) (agent.Execution, error) {
	execution, err := r.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{Execution: execution, definition: r}, nil
}

func (r *recordingDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	execution, err := r.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{Execution: execution, definition: r}, nil
}

type recordingExecution struct {
	agent.Execution
	definition *recordingDefinition
}

func (r *recordingExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	state, err := r.Snapshot()
	if err != nil {
		return agent.Transition{}, err
	}
	r.definition.samples = append(r.definition.samples, agenttest.ExecutionConformanceCase{
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
		{name: "step failure", wantCode: "example.child.failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompositionFixture(t, newFailingModelDeployment(t))
			config := agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}
			if test.refuse {
				refused := fixture.model.DeploymentRef()
				config.ProcessAdmitter = agent.ProcessAdmitterFunc(func(_ context.Context, admission agent.ProcessAdmission) error {
					if admission.DeploymentRef() == refused {
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
			result, err := engine.Run(t.Context(), fixture.composition, input)
			if err != nil || result.Termination().Status() != agent.StatusFailed {
				t.Fatalf("status=%s error=%v", result.Termination().Status(), err)
			}
			if failure, found := result.Termination().Failure(); !found || failure.Code() != test.wantCode {
				t.Fatalf("failure=%s, want %s", failure.Code(), test.wantCode)
			}
			if err := engine.ReleaseTree(t.Context(), result.ProcessID()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func newFailingModelDeployment(t *testing.T) agent.Deployment {
	t.Helper()
	model, err := newModelDeployment()
	if err != nil {
		t.Fatal(err)
	}
	failing, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           failingDefinition{Definition: model.Definition()},
		ImplementationDigest: agent.ComputeDigest([]byte("failing-composition-child")),
		ConfigurationDigest:  model.DeploymentRef().ConfigurationDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return failing
}

type failingDefinition struct{ agent.Definition }

func (f failingDefinition) Start(input agent.Payload) (agent.Execution, error) {
	execution, err := f.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return failingExecution{Execution: execution}, nil
}

func (f failingDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	execution, err := f.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return failingExecution{Execution: execution}, nil
}

type failingExecution struct{ agent.Execution }

func (f failingExecution) Step(context.Context, []agent.Signal) (agent.Transition, error) {
	failure, err := agent.NewFailure(agent.FailureKindExecution, "example.injected_failure", "composition child failed")
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Fail(0, failure)
}

func TestCompositionRejectsUnaddressedInputAtAdmission(t *testing.T) {
	fixture := newCompositionFixture(t, agent.Deployment{})
	base := fixture.composition
	input, err := base.Descriptor().EncodeInput(compositionInput{Prompt: "admission"})
	if err != nil {
		t.Fatal(err)
	}
	conformancetest.Run(t, agent.DeploymentConfig{
		Definition:           base.Definition(),
		ImplementationDigest: base.DeploymentRef().ImplementationDigest(),
		ConfigurationDigest:  base.DeploymentRef().ConfigurationDigest(),
	}, agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}, input)
}
