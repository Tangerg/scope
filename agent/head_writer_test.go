package agent

import "testing"

func TestHeadWriterStampsItsIdentityOnActivation(t *testing.T) {
	_, _, activation := treeCommitIdentityFixture(t)
	previous := activation.TreeSnapshot()
	foreign := controlValue(previous.wire())
	foreign.IncarnationID = activation.PreviousIncarnationID()
	committer := &recordingTreeCommitter{}
	writer := newHeadWriter(committer)
	activated := controlValue(writer.activate(t.Context(), previous, foreign))
	if activated.IncarnationID() != writer.incarnation() || writer.head().Digest() != activated.Digest() {
		t.Fatalf("activation head incarnation = %s, want this writer's %s", activated.IncarnationID(), writer.incarnation())
	}
	if activations := committer.treeActivations(); len(activations) != 1 || activations[0].TreeSnapshot().IncarnationID() != writer.incarnation() {
		t.Fatal("stored activation does not carry this writer's identity")
	}
}
