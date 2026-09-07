package main

import (
	"bytes"
	"context"
	"errors"
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

func TestUnknownChildSettlementSurvivesCompositionRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	local, err := newTextDeployment()
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
