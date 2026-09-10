package agent_test

import (
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestProcessRequiresEngineIssuedHandle(t *testing.T) {
	id, err := agent.ParseSignalID("signal:input")
	if err != nil {
		t.Fatal(err)
	}
	signal, err := agent.NewSignalRequest(id, agent.WaitID{}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		call func(*agent.Process)
	}{
		{"ID", func(process *agent.Process) { _ = process.ID() }},
		{"DeploymentRef", func(process *agent.Process) { _ = process.DeploymentRef() }},
		{"Relation", func(process *agent.Process) { _ = process.Relation() }},
		{"StartedAt", func(process *agent.Process) { _ = process.StartedAt() }},
		{"Budget", func(process *agent.Process) { _ = process.Budget() }},
		{"Capabilities", func(process *agent.Process) { _ = process.Capabilities() }},
		{"DeliverSignals", func(process *agent.Process) { _, _ = process.DeliverSignals(t.Context(), signal) }},
		{"Pause", func(process *agent.Process) { _ = process.Pause(t.Context(), "pause") }},
		{"Resume", func(process *agent.Process) { _ = process.Resume(t.Context()) }},
		{"RequestCancellation", func(process *agent.Process) { _ = process.RequestCancellation(t.Context(), "cancel") }},
		{"Kill", func(process *agent.Process) { _ = process.Kill(t.Context(), "kill") }},
		{"ResolveUnknownEffect", func(process *agent.Process) { _ = process.ResolveUnknownEffect(t.Context(), agent.Settlement{}) }},
		{"Await", func(process *agent.Process) { _, _ = process.Await(t.Context()) }},
		{"Join", func(process *agent.Process) { _ = process.Join(t.Context()) }},
	}
	for name, process := range map[string]*agent.Process{"nil": nil, "zero": {}} {
		for _, operation := range operations {
			t.Run(name+"/"+operation.name, func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Fatal("invalid Process handle did not panic")
					}
				}()
				operation.call(process)
			})
		}
	}
}
