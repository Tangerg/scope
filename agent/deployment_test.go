package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type deploymentTestDispatcher struct{}

func (*deploymentTestDispatcher) Dispatch(_ context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
	return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`{"ok":true}`))
}

func (*deploymentTestDispatcher) Policy(Effect) EffectPolicy {
	return EffectPolicy{Replay: ReplayPolicySameIdentity}
}

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
	relation := rootProcessRelation(processID)
	request := newEffectRequest(
		TreeIncarnationID{}, deployment.DeploymentRef(), relation, 1, 0, effect,
	)
	if incarnationID, durable := request.TreeIncarnationID(); durable || incarnationID.Valid() {
		t.Fatal("unbound request carries a writer identity")
	}
	copyOfEffect := request.Effect()
	copyOfEffect.payload[0] = '['
	if request.ProcessID() != processID || request.ID() != processID.effectID(1, 0) || request.DeploymentRef() != deployment.DeploymentRef() ||
		request.Relation() != relation || string(request.Effect().Payload()) != `{"operation":"test"}` {
		t.Fatalf("EffectRequest did not freeze Effect: %+v", request)
	}
	settlement, err := deployment.dispatcher.Dispatch(context.Background(), request, func(json.RawMessage) {})
	if err != nil || !settlement.Valid() {
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
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodePayload(engineTestInput{Value: "question"})
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusWaiting)
	tree, err := engine.CaptureTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, err := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(tree)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
	restored, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*Process{process, restored} {
		waitID, ok := inspectProcessSnapshot(t, candidate).WaitID()
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
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodePayload(engineTestInput{Value: "request"})
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
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodePayload(engineTestInput{Value: "request"})
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitForUnknownSettlement(t, process)
	tree, err := engine.CaptureTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, err := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(tree)})
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

// boundDefinition reports configurable child bindings over a leaf definition.
type boundDefinition struct {
	Definition
	children []Deployment
}

func (b *boundDefinition) ChildDeployments() []Deployment { return b.children }

func TestDeploymentIdentityCoversChildBindings(t *testing.T) {
	leaf := newEngineTestDefinition(t, "test.binding.leaf", "complete")
	first := newChildTestDeployment(t)
	second := engineTestDeployment(t, leaf, nil)
	deploy := func(children ...Deployment) (Deployment, error) {
		return NewDeployment(DeploymentConfig{
			Definition:           &boundDefinition{Definition: leaf, children: children},
			ImplementationDigest: ComputeDigest([]byte("binding implementation")),
			ConfigurationDigest:  ComputeDigest([]byte("binding configuration")),
		})
	}
	unbound := controlValue(deploy())
	one := controlValue(deploy(first))
	both := controlValue(deploy(first, second))
	if unbound.DeploymentRef() == one.DeploymentRef() || one.DeploymentRef() == both.DeploymentRef() {
		t.Fatal("different child bindings produced the same Deployment identity")
	}
	if unbound.DeploymentRef().ConfigurationDigest() != both.DeploymentRef().ConfigurationDigest() {
		t.Fatal("child bindings leaked into the Host configuration digest")
	}
	reordered := controlValue(deploy(second, first, second))
	if reordered.DeploymentRef() != both.DeploymentRef() || len(reordered.children) != 2 {
		t.Fatal("binding order or repetition changed the Deployment identity")
	}
	if _, err := deploy(first, Deployment{}); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("invalid child binding error = %v", err)
	}
}

func TestDeploymentRejectsChildBindingsThatChangeAfterConstruction(t *testing.T) {
	leaf := newEngineTestDefinition(t, "test.binding.drift", "complete")
	definition := &boundDefinition{Definition: leaf}
	deployment := controlValue(NewDeployment(DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: ComputeDigest([]byte("drift implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("drift configuration")),
	}))
	if err := deployment.validateDefinition(); err != nil {
		t.Fatal(err)
	}
	definition.children = []Deployment{newChildTestDeployment(t)}
	if err := deployment.validateDefinition(); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("changed child bindings were accepted: %v", err)
	}
}
