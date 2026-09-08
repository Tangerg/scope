package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/interaction"
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

func TestDefinitionsRejectInvalidBoundaryValues(t *testing.T) {
	if _, err := newCompositionDeployment(agent.DeploymentRef{}, agent.DeploymentRef{}); !errors.Is(err, agent.ErrInvalidDeploymentRef) {
		t.Fatalf("invalid child bindings: %v", err)
	}
	local, err := newUppercaseDeployment()
	if err != nil {
		t.Fatal(err)
	}
	model, err := newModelDeployment()
	if err != nil {
		t.Fatal(err)
	}
	composition, err := newCompositionDeployment(local.DeploymentRef(), model.DeploymentRef())
	if err != nil {
		t.Fatal(err)
	}
	for _, deployment := range []agent.Deployment{local, composition} {
		t.Run(deployment.Descriptor().Name(), func(t *testing.T) {
			input, err := agent.ParseInput([]byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, startErr := deployment.Definition().Start(input); startErr == nil {
				t.Error("Start accepted missing required input")
			}
			state, err := agent.NewExecutionState(deployment.Descriptor().Name(), []byte(`{"phase":"ready","prompt":"x","text":"x","unknown":true}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := deployment.Definition().Restore(state); err == nil {
				t.Error("Restore accepted unknown state fields")
			}
		})
	}
	for _, payload := range []string{
		`{"phase":"unknown","prompt":"x"}`,
		`{"phase":"ready","prompt":"x","child_ids":["child"]}`,
		`{"phase":"waiting_children","prompt":"x"}`,
		`{"phase":"awaiting_child_wait_open","prompt":"x","child_ids":["same","same"]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			state, err := agent.NewExecutionState("example.composition", []byte(payload))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := composition.Definition().Restore(state); err == nil {
				t.Fatal("Restore accepted contradictory execution state")
			}
		})
	}
}

func TestUnknownChildSettlementSurvivesCompositionRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	local, err := newUppercaseDeployment()
	if err != nil {
		t.Fatal(err)
	}
	model, dispatcher := newUncertainModelDeployment(t)
	composition, err := newCompositionDeployment(local.DeploymentRef(), model.DeploymentRef())
	if err != nil {
		t.Fatal(err)
	}
	resolver := deploymentResolver{local.DeploymentRef(): local, model.DeploymentRef(): model}
	observations := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{
		DeploymentResolver: resolver, EventListeners: []agent.EventListener{observations},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := composition.Descriptor().EncodeInput(compositionInput{Prompt: "recovered"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(ctx, composition, input)
	if err != nil {
		t.Fatal(err)
	}
	unknownEvent, err := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		fact, ok := event.EffectFinished()
		return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown
	})
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for root.Status() != agent.StatusWaiting {
		select {
		case <-ctx.Done():
			t.Fatalf("composition did not wait for the unknown child: %v", ctx.Err())
		case <-ticker.C:
		}
	}
	tree, err := engine.CaptureTree(ctx, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != 3 {
		t.Fatalf("captured Processes=%d, want one root and two children", len(tree.ProcessSnapshots()))
	}
	if killErr := root.Kill(ctx, "replace captured composition instance"); killErr != nil {
		t.Fatal(killErr)
	}
	if result, awaitErr := root.Await(ctx); awaitErr != nil || result.Status() != agent.StatusKilled {
		t.Fatalf("original root status=%s error=%v", result.Status(), awaitErr)
	}
	if releaseErr := engine.ReleaseTree(ctx, root.ID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	restoredEngine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := restoredEngine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	restored, err := restoredEngine.RestoreTree(ctx, composition, tree)
	if err != nil {
		t.Fatal(err)
	}
	child, found := restoredEngine.Process(unknownEvent.ProcessID())
	if !found {
		t.Fatal("restoration lost the model child identity")
	}
	unknown, err := child.UnknownEffectIDs(ctx)
	effectID, present := unknownEvent.EffectID()
	if err != nil || !present || len(unknown) != 1 || unknown[0] != effectID {
		t.Fatalf("unknown Effects=%v original=%s error=%v", unknown, effectID, err)
	}
	if restored.Status() != agent.StatusWaiting || dispatcher.calls.Load() != 1 {
		t.Fatalf("root status=%s dispatches=%d", restored.Status(), dispatcher.calls.Load())
	}
	// The external boundary supplies the recovered result; the Host never decodes
	// the Interaction payload or edits either Strategy's execution state.
	var settlement agent.Settlement
	select {
	case settlement = <-dispatcher.settlements:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if resolveErr := child.ResolveUnknownEffect(ctx, settlement); resolveErr != nil {
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
	finalTree, err := restoredEngine.CaptureTree(ctx, restored.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(finalTree.ProcessSnapshots()) != 3 || dispatcher.calls.Load() != 1 {
		t.Fatalf("final Processes=%d dispatches=%d", len(finalTree.ProcessSnapshots()), dispatcher.calls.Load())
	}
	for _, original := range tree.ProcessSnapshots() {
		process, found := restoredEngine.Process(original.ProcessID())
		if !found || process.Status() != agent.StatusCompleted {
			t.Fatalf("original Process %s was lost or did not complete", original.ProcessID())
		}
	}
	if releaseErr := restoredEngine.ReleaseTree(ctx, restored.ID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
}

func newUncertainModelDeployment(t *testing.T) (agent.Deployment, *lostResponseDispatcher) {
	t.Helper()
	client, err := chatclient.New(compositionModel{}, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.uncertain_model", Description: "Recover a lost composition response.", MaxModelCalls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Client: client})
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

func (*lostResponseDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

func TestCompositionRestoresEverySignalBoundary(t *testing.T) {
	local, err := newUppercaseDeployment()
	if err != nil {
		t.Fatal(err)
	}
	model, err := newModelDeployment()
	if err != nil {
		t.Fatal(err)
	}
	base, err := newCompositionDeployment(local.DeploymentRef(), model.DeploymentRef())
	if err != nil {
		t.Fatal(err)
	}
	definition := &recordingDefinition{Definition: base.Definition()}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: base.DeploymentRef().ImplementationDigest(),
		ConfigurationDigest:  base.DeploymentRef().ConfigurationDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deploymentResolver{
		local.DeploymentRef(): local, model.DeploymentRef(): model,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := base.Descriptor().EncodeInput(compositionInput{Prompt: "boundaries"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Status(), err)
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: base.Definition(), Input: input, RestoredCases: definition.samples,
	})
	var opening, completion agenttest.ExecutionConformanceCase
	for _, sample := range definition.samples {
		var state compositionState
		if err := json.Unmarshal(sample.State.Payload(), &state); err != nil {
			t.Fatal(err)
		}
		switch state.Phase {
		case compositionAwaitingChildWaitOpen:
			opening = sample
		case compositionWaitingChildren:
			completion = sample
		}
		t.Run(sample.Name+" rejects unexpected first signal", func(t *testing.T) {
			execution, err := base.Definition().Restore(sample.State)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execution.Step(t.Context(), append([]agent.Signal{{}}, sample.Signals...)); err == nil {
				t.Fatal("unexpected first Signal was discarded")
			}
		})
	}
	if !opening.State.Valid() || !completion.State.Valid() {
		t.Fatal("execution did not visit both child wait boundaries")
	}
	for _, sample := range []agenttest.ExecutionConformanceCase{opening, completion} {
		t.Run(sample.Name+" consumes only its prefix", func(t *testing.T) {
			execution, err := base.Definition().Restore(sample.State)
			if err != nil {
				t.Fatal(err)
			}
			transition, err := execution.Step(t.Context(), append(sample.Signals[:1:1], completion.Signals[0]))
			if err != nil || transition.ConsumedSignals() != 1 {
				t.Fatalf("consumed=%d error=%v", transition.ConsumedSignals(), err)
			}
			if sample.Name == opening.Name && transition.Kind() != agent.TransitionKindWait {
				t.Fatalf("kind=%s, want wait", transition.Kind())
			}
		})
		t.Run(sample.Name+" rejects unrelated wait", func(t *testing.T) {
			execution, err := base.Definition().Restore(sample.State)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(sample.Signals[0])
			if err != nil {
				t.Fatal(err)
			}
			encoded = bytes.ReplaceAll(encoded, []byte(`"composition"`), []byte(`"unrelated"`))
			var signal agent.Signal
			if err := json.Unmarshal(encoded, &signal); err != nil {
				t.Fatal(err)
			}
			if _, err := execution.Step(t.Context(), []agent.Signal{signal}); err == nil {
				t.Fatal("unrelated wait was accepted")
			}
		})
	}
	if err := engine.ReleaseTree(t.Context(), result.ProcessID()); err != nil {
		t.Fatal(err)
	}
}

type recordingDefinition struct {
	agent.Definition
	samples []agenttest.ExecutionConformanceCase
}

func (r *recordingDefinition) Start(input agent.Input) (agent.Execution, error) {
	execution, err := r.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{Execution: execution, definition: r}, nil
}

func (r *recordingDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := r.Definition.Restore(state)
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
	for _, failStart := range []bool{true, false} {
		t.Run(fmt.Sprintf("start failure %t", failStart), func(t *testing.T) {
			local, err := newUppercaseDeployment()
			if err != nil {
				t.Fatal(err)
			}
			model, err := newModelDeployment()
			if err != nil {
				t.Fatal(err)
			}
			if !failStart {
				model, err = agent.NewDeployment(agent.DeploymentConfig{
					Definition:           failingDefinition{Definition: model.Definition()},
					ImplementationDigest: agent.ComputeDigest([]byte("failing-composition-child")),
					ConfigurationDigest:  model.DeploymentRef().ConfigurationDigest(),
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			composition, err := newCompositionDeployment(local.DeploymentRef(), model.DeploymentRef())
			if err != nil {
				t.Fatal(err)
			}
			resolver := deploymentResolver{local.DeploymentRef(): local}
			if !failStart {
				resolver[model.DeploymentRef()] = model
			}
			engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, err := composition.Descriptor().EncodeInput(compositionInput{Prompt: "failure"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Run(t.Context(), composition, input)
			if err != nil || result.Status() != agent.StatusFailed {
				t.Fatalf("status=%s error=%v", result.Status(), err)
			}
			failure, found := result.Termination().Failure()
			wantCode := "example.child.failed"
			if failStart {
				wantCode = "engine.child.deployment_unavailable"
			}
			if !found || failure.Code() != wantCode {
				t.Fatalf("failure=%s, want %s", failure.Code(), wantCode)
			}
			if err := engine.ReleaseTree(t.Context(), result.ProcessID()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type failingDefinition struct{ agent.Definition }

func (f failingDefinition) Start(input agent.Input) (agent.Execution, error) {
	execution, err := f.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return failingExecution{Execution: execution}, nil
}

func (f failingDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := f.Definition.Restore(state)
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
