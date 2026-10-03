package agent

import "testing"

func TestHeadWriterRejectsForeignActivationBeforeStorage(t *testing.T) {
	_, _, activation := treeCommitIdentityFixture(t)
	wire := controlValue(activation.TreeSnapshot().wire())
	wire.IncarnationID = activation.PreviousIncarnationID()
	previous := controlValue(newTreeSnapshot(wire))
	committer := &recordingTreeCommitter{}
	writer := newHeadWriter(committer)
	if err := writer.activate(t.Context(), previous, activation.TreeSnapshot()); err == nil {
		t.Fatal("activation accepted another writer's snapshot")
	}
	if len(committer.treeActivations()) != 0 || writer.head().Valid() {
		t.Fatal("rejected activation changed the durable or acknowledged head")
	}
}
