package workflow

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestCompileFreezesCallbackSettingsAndIncludesTheirIdentity(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	stage := mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })
	settings := map[string]int{"threshold": 2}
	config := Config{Name: "test.settings", Description: "Bind declared callback settings.", Revision: "policy-v1", Configuration: settings, Stages: []Stage{stage}}
	first, err := Compile[int, int](compiler, config)
	if err != nil {
		t.Fatal(err)
	}
	firstRef := first.ProgramRef()
	settings["threshold"] = 3
	second, err := Compile[int, int](compiler, config)
	if err != nil {
		t.Fatal(err)
	}
	if firstRef == second.ProgramRef() || first.ProgramRef() != firstRef {
		t.Fatal("callback settings did not freeze into the exact program identity")
	}
	settings["threshold"] = 2
	same, err := Compile[int, int](compiler, config)
	if err != nil {
		t.Fatal(err)
	}
	if same.ProgramRef() != firstRef {
		t.Fatal("identical declarative settings produced another program")
	}
	config.Revision = "policy-v2"
	revised, err := Compile[int, int](compiler, config)
	if err != nil {
		t.Fatal(err)
	}
	if revised.ProgramRef() == firstRef {
		t.Fatal("opaque callback revision did not change identity")
	}
	config.Configuration = make(chan int)
	if _, err := Compile[int, int](compiler, config); err == nil {
		t.Fatal("unserializable callback settings were not rejected")
	}
}

func TestDeclarativeGraphRejectsUnownedCallbackConfiguration(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	child := mustProgram(t, mustDefinition(t, "test.identity", mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })), "identity")
	call, err := Call(CallConfig{ID: "child", Child: agentrun.ChildBinding{Program: child}})
	if err != nil {
		t.Fatal(err)
	}
	if _, compileErr := Compile[int, int](compiler, Config{Name: "test.call", Description: "Use one declarative child.", Stages: []Stage{call}, Configuration: struct{}{}}); compileErr == nil {
		t.Fatal("graph with no callback admitted callback configuration")
	}
	program, err := Compile[int, int](compiler, Config{Name: "test.call", Description: "Use one declarative child.", Stages: []Stage{call}})
	if err != nil || !program.Valid() {
		t.Fatal(err)
	}
	topology, err := TopologyOf(program)
	if err != nil || len(topology.Stages) != 1 || topology.Stages[0].Bindings[0].ProgramRef != child.ProgramRef() {
		t.Fatalf("public topology lost the compiled child binding: %+v %v", topology, err)
	}
}
