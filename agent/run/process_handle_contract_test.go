package run_test

import (
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestProcessRequiresEngineIssuedHandle(t *testing.T) {
	id, err := run.ParseSignalID("signal:input")
	if err != nil {
		t.Fatal(err)
	}
	signal, err := run.NewSignalRequest(id, run.WaitID{}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		call func(*run.Handle[run.Payload])
	}{
		{"ID", func(process *run.Handle[run.Payload]) { _ = process.Relation().ProcessID() }},
		{"ProgramRef", func(process *run.Handle[run.Payload]) { _ = process.ProgramRef() }},
		{"Relation", func(process *run.Handle[run.Payload]) { _ = process.Relation() }},
		{"StartedAt", func(process *run.Handle[run.Payload]) { _ = process.StartedAt() }},
		{"Budget", func(process *run.Handle[run.Payload]) { _ = process.Budget() }},
		{"Capabilities", func(process *run.Handle[run.Payload]) { _ = process.Capabilities() }},
		{"DeliverSignals", func(process *run.Handle[run.Payload]) { _, _ = process.DeliverSignals(t.Context(), signal) }},
		{"Pause", func(process *run.Handle[run.Payload]) { _ = process.Pause(t.Context(), "pause") }},
		{"Resume", func(process *run.Handle[run.Payload]) { _ = process.Resume(t.Context()) }},
		{"Cancel", func(process *run.Handle[run.Payload]) { _ = process.Cancel(t.Context(), "cancel") }},
		{"Kill", func(process *run.Handle[run.Payload]) { _ = process.Kill(t.Context(), "kill") }},
		{"ResolveUnknownEffect", func(process *run.Handle[run.Payload]) {
			_ = process.ResolveUnknownEffect(t.Context(), run.EffectID{}, run.Settlement{})
		}},
		{"Await", func(process *run.Handle[run.Payload]) { _, _ = process.Await(t.Context()) }},
		{"Join", func(process *run.Handle[run.Payload]) { _ = process.Join(t.Context()) }},
	}
	for name, process := range map[string]*run.Handle[run.Payload]{"nil": nil, "zero": {}} {
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
