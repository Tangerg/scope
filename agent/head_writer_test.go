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

func TestHeadWriterAcceptsOnlyTheCurrentCommitAcknowledgment(t *testing.T) {
	runtime, _, prospective := effectBoundaryFixture(t, 1, 64)
	writer := runtime.writer
	writer.committer = &recordingTreeCommitter{}
	previous := writer.head()
	commit := &treeCommit{kind: treeCommitCheckpoint, snapshot: prospective}
	if err := writer.commitCheckpoint(t.Context(), commit, checkpointCauseCut); err != nil {
		t.Fatal(err)
	}
	completion := receiveTreeRuntimeProbe(t, writer.done)
	stale := treeCommitCompletion{commit: &treeCommit{kind: commit.kind, snapshot: prospective}}
	if accepted, current := writer.settle(stale); current || accepted != nil || !writer.committing() || writer.head().Digest() != previous.Digest() || writer.sequence != 0 {
		t.Fatal("another commit's acknowledgment displaced the current durable write")
	}
	if accepted, current := writer.settle(completion); !current || accepted != commit || completion.err != nil || writer.committing() || writer.head().Digest() != prospective.Digest() || writer.sequence != 1 {
		t.Fatal("current acknowledgment did not adopt exactly one durable head")
	}
	if accepted, current := writer.settle(completion); current || accepted != nil || writer.sequence != 1 {
		t.Fatal("duplicate acknowledgment advanced the durable sequence")
	}
}
