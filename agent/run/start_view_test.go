package run

import (
	"testing"
)

func TestStartedTreeReadNeverRegistersOrActivatesAWriter(t *testing.T) {
	store := NewMemoryStore()
	leader := controlValue(New(t.Context(), Config{Store: store}))
	reader := controlValue(New(t.Context(), Config{Store: store}))
	views := controlValue(reader.StartedTrees())
	id := StartID{Scope: "tenant-a", Key: "wire-id"}
	p := controlValue(leader.Start(t.Context(), newChildTestDeployment(t), StartRequest[Payload]{ID: id, Input: controlValue(EncodePayload(childTestInput{Mode: "leaf"}))}))
	_ = mustAwait(t, p)
	head, found, err := views.Load(t.Context(), id)
	if err != nil || !found || head.RootID() != p.ID() || head.IncarnationID() != p.TreeIncarnationID() {
		t.Fatal("unrecognized durable activation", err)
	}
	if _, found := reader.Process(p.ID()); found {
		t.Fatal("read installed a writer")
	}
	if _, found, err := views.Load(t.Context(), StartID{Scope: "tenant-b", Key: id.Key}); err != nil || found {
		t.Fatal("foreign scope could read the head", err)
	}
	mustCloseEngine(t, leader)
	mustCloseEngine(t, reader)
}
