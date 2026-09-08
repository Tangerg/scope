package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type deploymentTestDispatcher struct{}

func (*deploymentTestDispatcher) Dispatch(_ context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{"ok":true}`))
}

func (*deploymentTestDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicySameIdentity }

func TestDeploymentBindsExactDefinitionAndDispatcher(t *testing.T) {
	definition := newTypedFixtureDefinition[wireFixture](t, "deployment.fixture")
	deployment, err := NewDeployment(DeploymentConfig{
		Definition:           definition,
		Dispatcher:           &deploymentTestDispatcher{},
		ImplementationDigest: ComputeDigest([]byte("deployment fixture implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("deployment fixture configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !deployment.Valid() || deployment.DeploymentRef().ContractDigest() != definition.Descriptor().Digest() || deployment.Definition() != definition {
		t.Fatalf("Deployment = %+v", deployment)
	}

	effect, err := NewDispatcherEffect(json.RawMessage(`{"operation":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	processID, err := ParseProcessID("process:deployment")
	if err != nil {
		t.Fatal(err)
	}
	effectID, err := ParseEffectID("process:deployment:step:1:effect:0")
	if err != nil {
		t.Fatal(err)
	}
	relation := rootProcessRelation(processID)
	request := newEffectRequest(
		processID, TreeIncarnationID{}, deployment.DeploymentRef(), relation, 1, 0, effectID, effect,
	)
	if incarnationID, durable := request.TreeIncarnationID(); durable || incarnationID.Valid() {
		t.Fatal("ephemeral request carries durable writer identity")
	}
	copyOfEffect := request.Effect()
	copyOfEffect.payload[0] = '['
	if request.ProcessID() != processID || request.DeploymentRef() != deployment.DeploymentRef() ||
		request.Relation() != relation || string(request.Effect().Payload()) != `{"operation":"test"}` {
		t.Fatalf("EffectRequest did not freeze Effect: %+v", request)
	}
	settlement, err := deployment.effectDispatcher().Dispatch(context.Background(), request, func(json.RawMessage) {})
	if err != nil || settlement.EffectID() != request.ID() {
		t.Fatalf("Dispatch settlement = %+v, %v", settlement, err)
	}
}

func TestDeploymentRejectsMissingOrTypedNilBindings(t *testing.T) {
	definition := newTypedFixtureDefinition[wireFixture](t, "deployment.fixture")
	valid := DeploymentConfig{
		Definition:           definition,
		Dispatcher:           &deploymentTestDispatcher{},
		ImplementationDigest: ComputeDigest([]byte("implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("configuration")),
	}
	var nilDefinition *typedFixtureDefinition
	var nilDispatcher *deploymentTestDispatcher
	for _, config := range []DeploymentConfig{
		{},
		{Definition: nilDefinition, Dispatcher: valid.Dispatcher, ImplementationDigest: valid.ImplementationDigest, ConfigurationDigest: valid.ConfigurationDigest},
		{Definition: valid.Definition, Dispatcher: nilDispatcher, ImplementationDigest: valid.ImplementationDigest, ConfigurationDigest: valid.ConfigurationDigest},
	} {
		if _, err := NewDeployment(config); !errors.Is(err, ErrInvalidDeployment) {
			t.Fatalf("NewDeployment error = %v, want ErrInvalidDeployment", err)
		}
	}
}

func TestDeploymentWithoutDispatcherRunsAndRestoresFrameworkEffects(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, nil)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodeInput(engineTestInput{Value: "question"})
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusWaiting)
	tree, err := engine.CaptureTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
	restored, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*Process{process, restored} {
		waitID, ok := candidate.WaitID()
		if !ok {
			t.Fatal("framework wait did not survive restoration")
		}
		id, _ := ParseSignalID("signal:answer")
		answer, _ := NewSignalRequest(id, waitID, json.RawMessage(`{"kind":"answer","value":"approved"}`))
		if accepted, err := candidate.DeliverSignals(t.Context(), answer); err != nil || !accepted {
			t.Fatalf("answer accepted=%t, error=%v", accepted, err)
		}
		result := awaitResult(t, candidate)
		output, ok := result.Output()
		value, err := output.Decode[engineTestOutput]()
		if result.Status() != StatusCompleted || !ok || err != nil || value.Value != "approved" {
			t.Fatalf("framework completion status=%s, output=%+v, error=%v", result.Status(), value, err)
		}
	}
}

func TestDeploymentWithoutDispatcherRejectsWholeExternalEffectBatch(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.batch", "batch")
	deployment := engineTestDeployment(t, definition, nil)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodeInput(engineTestInput{Value: "request"})
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, ok := result.Termination().Failure()
	if result.Status() != StatusFailed || !ok || failure.Kind() != FailureKindContract ||
		failure.Code() != "execution.effect.invalid" || result.Usage().PreparedEffects != 0 {
		t.Fatalf("unbound Effect result=%+v, failure=%+v", result, failure)
	}
}

func TestDeploymentWithoutDispatcherRejectsRestoredExternalEffect(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	dispatcher := &failingEngineTestDispatcher{}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodeInput(engineTestInput{Value: "request"})
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitForUnknownSettlement(t, process)
	tree, err := engine.CaptureTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
	withoutDispatcher := engineTestDeployment(t, definition, nil)
	if restored, err := restoredEngine.RestoreTree(t.Context(), withoutDispatcher, tree); !errors.Is(err, ErrInvalidSnapshot) || !errors.Is(err, ErrInvalidEffect) || restored != nil {
		t.Fatalf("restored=%v, error=%v", restored, err)
	}
	if dispatcher.calls.Load() != 1 {
		t.Fatalf("restoration repeated %d external calls", dispatcher.calls.Load())
	}
	if err := process.Kill(t.Context(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	_ = awaitResult(t, process)
}
