package workflow_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

type expectedTopologyBinding struct {
	role       workflow.BindingRole
	id         string
	deployment agent.Deployment
}

type topologyCase struct {
	name          string
	stage         workflow.Stage
	kind          workflow.StageKind
	bindings      []expectedTopologyBinding
	windowSize    uint32
	maxItems      uint32
	maxIterations uint32
}

func TestDefinitionTopologyProjectsEverySealedStageKind(t *testing.T) {
	numberChild := mustTopologyDeployment(
		t,
		"test.topology.number",
		func(input numberInput) (numberOutput, error) {
			return numberOutput(input), nil
		},
	)
	alternateNumberChild := mustTopologyDeployment(
		t,
		"test.topology.alternate_number",
		func(input numberInput) (numberOutput, error) {
			return numberOutput(input), nil
		},
	)
	identityChild := mustTopologyDeployment(
		t,
		"test.topology.identity",
		func(input numberInput) (numberInput, error) { return input, nil },
	)
	budget := mustBudget(t)
	capability, err := agent.ParseCapability("test.topology.execute")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := agent.NewCapabilitySet(capability)
	if err != nil {
		t.Fatal(err)
	}

	transform := mustTransform(t, "transform", func(input numberInput) (numberOutput, error) {
		return numberOutput(input), nil
	})
	call, err := workflow.Call(workflow.CallConfig{
		ID: "call", Deployment: numberChild, Budget: budget, Capabilities: capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	switcher, err := workflow.Switch(workflow.SwitchConfig[numberInput]{
		ID:     "switch",
		Select: func(numberInput) (string, error) { return "primary", nil },
		Cases: []workflow.SwitchCase{
			{ID: "primary", Deployment: numberChild, Budget: budget, Capabilities: capabilities},
			{ID: "fallback", Deployment: alternateNumberChild, Budget: budget, Capabilities: capabilities},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := workflow.Fork(workflow.ForkConfig[numberInput, numberOutput, numberOutput]{
		ID: "fork",
		Branches: []workflow.ForkBranch{
			{ID: "left", Deployment: numberChild, Budget: budget, Capabilities: capabilities},
			{ID: "right", Deployment: alternateNumberChild, Budget: budget, Capabilities: capabilities},
		},
		WindowSize: 1,
		Reduce:     func(outputs []numberOutput) (numberOutput, error) { return outputs[0], nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	mapper, err := workflow.Map[numberInput, numberOutput](workflow.MapConfig[numberInput, numberOutput]{
		ID: "map", Deployment: numberChild, Budget: budget, Capabilities: capabilities,
		WindowSize: 2, MaxItems: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	loop, err := workflow.Loop(workflow.LoopConfig[numberInput]{
		ID: "loop", Body: identityChild, Budget: budget, Capabilities: capabilities, MaxIterations: 3,
		Predicate: func(numberInput) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []topologyCase{
		{name: "transform", stage: transform, kind: workflow.StageKindTransform},
		{name: "call", stage: call, kind: workflow.StageKindCall, bindings: []expectedTopologyBinding{
			{role: workflow.BindingRoleCall, deployment: numberChild},
		}},
		{name: "switch", stage: switcher, kind: workflow.StageKindSwitch, bindings: []expectedTopologyBinding{
			{role: workflow.BindingRoleCase, id: "primary", deployment: numberChild},
			{role: workflow.BindingRoleCase, id: "fallback", deployment: alternateNumberChild},
		}},
		{name: "fork", stage: fork, kind: workflow.StageKindFork, bindings: []expectedTopologyBinding{
			{role: workflow.BindingRoleBranch, id: "left", deployment: numberChild},
			{role: workflow.BindingRoleBranch, id: "right", deployment: alternateNumberChild},
		}, windowSize: 1},
		{name: "map", stage: mapper, kind: workflow.StageKindMap, bindings: []expectedTopologyBinding{
			{role: workflow.BindingRoleItem, deployment: numberChild},
		}, windowSize: 2, maxItems: 4},
		{name: "loop", stage: loop, kind: workflow.StageKindLoop, bindings: []expectedTopologyBinding{
			{role: workflow.BindingRoleBody, deployment: identityChild},
		}, maxIterations: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertTopologyCase(t, test, budget, capabilities)
		})
	}
}

func assertTopologyCase(
	t *testing.T,
	test topologyCase,
	budget agent.Budget,
	capabilities agent.CapabilitySet,
) {
	t.Helper()
	definition := mustDefinition(t, "test.topology."+test.name, test.stage)
	topology := definition.Topology()
	if topology.Descriptor.Digest() != definition.Descriptor().Digest() || len(topology.Stages) != 1 {
		t.Fatalf("topology=%+v", topology)
	}
	stage := topology.Stages[0]
	if stage.Kind != test.kind || stage.WindowSize != test.windowSize ||
		stage.MaxItems != test.maxItems || stage.MaxIterations != test.maxIterations {
		t.Fatalf("stage=%+v", stage)
	}
	if stage.ID != test.name || !stage.InputSchema.Valid() ||
		!stage.OutputSchema.Valid() || len(stage.Bindings) != len(test.bindings) {
		t.Fatalf("stage contracts=%+v", stage)
	}
	for index, want := range test.bindings {
		assertTopologyBinding(t, index, stage.Bindings[index], want, budget, capabilities)
	}
	wire, err := json.Marshal(topology)
	if err != nil {
		t.Fatalf("marshal Topology: %v", err)
	}
	if test.kind == workflow.StageKindMap {
		var projection struct {
			Stages []struct {
				MaxItems uint32 `json:"max_items"`
			} `json:"stages"`
		}
		if err := json.Unmarshal(wire, &projection); err != nil {
			t.Fatalf("unmarshal Topology projection: %v", err)
		}
		if len(projection.Stages) != 1 || projection.Stages[0].MaxItems != test.maxItems {
			t.Fatalf("wire Map maximum=%+v", projection.Stages)
		}
	}
}

func assertTopologyBinding(
	t *testing.T,
	index int,
	binding workflow.BindingTopology,
	want expectedTopologyBinding,
	budget agent.Budget,
	capabilities agent.CapabilitySet,
) {
	t.Helper()
	descriptor := want.deployment.Descriptor()
	if binding.Role != want.role || binding.ID != want.id ||
		binding.DeploymentRef != want.deployment.DeploymentRef() ||
		!bytes.Equal(binding.InputSchema.JSON(), descriptor.InputSchema().JSON()) ||
		!bytes.Equal(binding.OutputSchema.JSON(), descriptor.OutputSchema().JSON()) ||
		binding.Budget != budget ||
		!slices.Equal(binding.Capabilities.Values(), capabilities.Values()) {
		t.Fatalf("binding[%d]=%+v", index, binding)
	}
}

func TestDefinitionTopologyOwnsProjectionSlices(t *testing.T) {
	child := mustTopologyDeployment(
		t,
		"test.topology.ownership_child",
		func(input numberInput) (numberOutput, error) { return numberOutput(input), nil },
	)
	stage, err := workflow.Switch(workflow.SwitchConfig[numberInput]{
		ID:     "route",
		Select: func(numberInput) (string, error) { return "first", nil },
		Cases: []workflow.SwitchCase{
			{ID: "first", Deployment: child, Budget: mustBudget(t)},
			{ID: "second", Deployment: child, Budget: mustBudget(t)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, "test.topology.ownership", stage)

	first := definition.Topology()
	first.Stages[0].ID = "changed"
	first.Stages[0].Bindings[0].ID = "changed"
	second := definition.Topology()
	if second.Stages[0].ID != "route" || second.Stages[0].Bindings[0].ID != "first" {
		t.Fatalf("later projection was mutated: %+v", second.Stages[0])
	}
}

func TestNilDefinitionTopologyIsZero(t *testing.T) {
	var definition *workflow.Definition
	if topology := definition.Topology(); topology.Descriptor.Valid() || len(topology.Stages) != 0 {
		t.Fatalf("nil Definition Topology=%+v", topology)
	}
}

func mustTopologyDeployment[I, O any](
	t *testing.T,
	name string,
	transform workflow.TransformFunc[I, O],
) agent.Deployment {
	t.Helper()
	stage, err := workflow.Transform("apply", transform)
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, name, stage)
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(name + ":implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(name + ":configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}
