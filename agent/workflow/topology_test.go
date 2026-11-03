package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

type expectedTopologyBinding struct {
	role    BindingRole
	id      string
	program run.Program[run.Payload, run.Payload]
}

type topologyCase struct {
	name          string
	stage         Stage
	kind          StageKind
	bindings      []expectedTopologyBinding
	windowSize    uint32
	maxItems      uint32
	maxIterations uint32
}

func TestDefinitionTopologyProjectsEverySealedStageKind(t *testing.T) {
	numberChild := mustTopologyProgram(
		t,
		"test.topology.number",
		func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput(input), nil
		},
	)
	alternateNumberChild := mustTopologyProgram(
		t,
		"test.topology.alternate_number",
		func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput(input), nil
		},
	)
	identityChild := mustTopologyProgram(
		t,
		"test.topology.identity",
		func(_ context.Context, input numberInput) (numberInput, error) { return input, nil },
	)
	budget := mustBudget(t)
	capability, err := run.ParseCapability("test.topology.execute")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := run.NewCapabilitySet(capability)
	if err != nil {
		t.Fatal(err)
	}

	transform := mustTransform(t, "transform", func(_ context.Context, input numberInput) (numberOutput, error) {
		return numberOutput(input), nil
	})
	call, err := Call(CallConfig{
		ID: "call", Child: run.ChildBinding{Program: numberChild, Budget: budget, Capabilities: capabilities},
	})
	if err != nil {
		t.Fatal(err)
	}
	switcher, err := Switch(SwitchConfig[numberInput]{
		ID:     "switch",
		Select: func(context.Context, numberInput) (string, error) { return "primary", nil },
		Cases: []SwitchCase{
			{ID: "primary", Child: run.ChildBinding{Program: numberChild, Budget: budget, Capabilities: capabilities}},
			{ID: "fallback", Child: run.ChildBinding{Program: alternateNumberChild, Budget: budget, Capabilities: capabilities}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := Fork(ForkConfig[numberInput, numberOutput, numberOutput]{
		ID: "fork",
		Branches: []ForkBranch{
			{ID: "left", Child: run.ChildBinding{Program: numberChild, Budget: budget, Capabilities: capabilities}},
			{ID: "right", Child: run.ChildBinding{Program: alternateNumberChild, Budget: budget, Capabilities: capabilities}},
		},
		WindowSize: 1,
		Reduce: func(_ context.Context, _ numberInput, outputs []numberOutput) (numberOutput, error) {
			return outputs[0], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapper, err := Map[numberInput, numberOutput](MapConfig[numberInput, numberOutput]{
		ID: "map", Child: run.ChildBinding{Program: numberChild, Budget: budget, Capabilities: capabilities},
		WindowSize: 2, MaxItems: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	loop, err := Loop(LoopConfig[numberInput]{
		ID: "loop", Body: run.ChildBinding{Program: identityChild, Budget: budget, Capabilities: capabilities}, MaxIterations: run.NewQuota(3),
		Predicate: func(context.Context, numberInput) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []topologyCase{
		{name: "transform", stage: transform, kind: StageKindTransform},
		{name: "call", stage: call, kind: StageKindCall, bindings: []expectedTopologyBinding{
			{role: BindingRoleCall, program: numberChild},
		}},
		{name: "switch", stage: switcher, kind: StageKindSwitch, bindings: []expectedTopologyBinding{
			{role: BindingRoleCase, id: "primary", program: numberChild},
			{role: BindingRoleCase, id: "fallback", program: alternateNumberChild},
		}},
		{name: "fork", stage: fork, kind: StageKindFork, bindings: []expectedTopologyBinding{
			{role: BindingRoleBranch, id: "left", program: numberChild},
			{role: BindingRoleBranch, id: "right", program: alternateNumberChild},
		}, windowSize: 1},
		{name: "map", stage: mapper, kind: StageKindMap, bindings: []expectedTopologyBinding{
			{role: BindingRoleItem, program: numberChild},
		}, windowSize: 2, maxItems: 4},
		{name: "loop", stage: loop, kind: StageKindLoop, bindings: []expectedTopologyBinding{
			{role: BindingRoleBody, program: identityChild},
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
	budget run.Budget,
	capabilities run.CapabilitySet,
) {
	t.Helper()
	definition := mustDefinition(t, "test.topology."+test.name, test.stage)
	topology := definition.Topology()
	if topology.Descriptor.Digest() != definition.Descriptor().Digest() || len(topology.Stages) != 1 {
		t.Fatalf("topology=%+v", topology)
	}
	stage := topology.Stages[0]
	wantIterations := run.Quota{}
	if test.maxIterations != 0 {
		wantIterations = run.NewQuota(uint64(test.maxIterations))
	}
	if stage.Kind != test.kind || stage.WindowSize != test.windowSize ||
		stage.MaxItems != test.maxItems || (stage.MaxIterations != nil) != (test.kind == StageKindLoop) ||
		stage.MaxIterations != nil && *stage.MaxIterations != wantIterations {
		t.Fatalf("stage=%+v", stage)
	}
	if stage.ID != test.name || !stage.InputSchema.Valid() ||
		!stage.OutputSchema.Valid() || len(stage.Bindings) != len(test.bindings) {
		t.Fatalf("stage contracts=%+v", stage)
	}
	for index, want := range test.bindings {
		assertTopologyBinding(t, index, stage.Bindings[index], want, budget, capabilities)
	}
	wire, err := jsonv2.Marshal(topology)
	if err != nil {
		t.Fatalf("marshal Topology: %v", err)
	}
	if test.kind == StageKindMap {
		var projection struct {
			Stages []struct {
				MaxItems uint32 `json:"max_items"`
			} `json:"stages"`
		}
		if err := jsonv2.Unmarshal(wire, &projection); err != nil {
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
	binding BindingTopology,
	want expectedTopologyBinding,
	budget run.Budget,
	capabilities run.CapabilitySet,
) {
	t.Helper()
	descriptor := want.program.Descriptor()
	if binding.Role != want.role || binding.ID != want.id ||
		binding.ProgramRef != want.program.ProgramRef() ||
		!bytes.Equal(binding.InputSchema.JSON(), descriptor.InputSchema().JSON()) ||
		!bytes.Equal(binding.OutputSchema.JSON(), descriptor.OutputSchema().JSON()) ||
		binding.Budget != budget ||
		!slices.Equal(binding.Capabilities.Values(), capabilities.Values()) {
		t.Fatalf("binding[%d]=%+v", index, binding)
	}
}

func TestDefinitionTopologyOwnsProjectionSlices(t *testing.T) {
	child := mustTopologyProgram(
		t,
		"test.topology.ownership_child",
		func(_ context.Context, input numberInput) (numberOutput, error) { return numberOutput(input), nil },
	)
	stage, err := Switch(SwitchConfig[numberInput]{
		ID:     "route",
		Select: func(context.Context, numberInput) (string, error) { return "first", nil },
		Cases: []SwitchCase{
			{ID: "first", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}},
			{ID: "second", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}},
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
	var definition *definition
	if topology := definition.Topology(); topology.Descriptor.Valid() || len(topology.Stages) != 0 {
		t.Fatalf("nil definition Topology=%+v", topology)
	}
}

func mustTopologyProgram[I, O any](
	t *testing.T,
	name string,
	transform TransformFunc[I, O],
) run.Program[run.Payload, run.Payload] {
	t.Helper()
	stage, err := Transform("apply", transform)
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, name, stage)
	program, err := run.Bind[run.Payload, run.Payload](conformancetest.Compiler(t), run.BindingConfig{
		Definition:    definition,
		Revision:      name + ":implementation",
		Configuration: name + ":configuration",
	})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func TestTopologyLimitPresence(t *testing.T) {
	for _, test := range []struct {
		name    string
		maximum *run.Quota
		want    string
	}{
		{"non-loop", nil, ""},
		{"unlimited loop", new(run.Quota{}), `{"maximum":null}`},
		{"finite loop", new(run.NewQuota(3)), `{"maximum":3}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema, err := jsonschema.For[int]()
			if err != nil {
				t.Fatal(err)
			}
			stage := StageTopology{ID: "test", Kind: StageKindTransform, InputSchema: schema, OutputSchema: schema, MaxIterations: test.maximum}
			data, err := jsonv2.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := jsonv2.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["max_iterations"]) != test.want || fields["window_size"] != nil || fields["max_items"] != nil {
				t.Fatalf("unexpected projected limits: %s", data)
			}
		})
	}
}
