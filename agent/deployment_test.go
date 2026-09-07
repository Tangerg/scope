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
