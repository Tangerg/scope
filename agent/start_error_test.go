package agent

import (
	"context"
	"errors"
	"testing"
)

// lostAckStartCommitter persists the initial checkpoint but loses the
// acknowledgment the caller needed, modeling a crash between durable write and
// reply.
type lostAckStartCommitter struct {
	*MemoryTreeCommitter
}

func (l *lostAckStartCommitter) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	if checkpoint.Kind() == TreeCheckpointKindStart {
		_ = l.MemoryTreeCommitter.CommitCheckpoint(ctx, checkpoint)
		return errors.New("lost acknowledgment")
	}
	return l.MemoryTreeCommitter.CommitCheckpoint(ctx, checkpoint)
}

func TestStartReportsReconcilableIdentityWhenInitialCommitIsUnacknowledged(t *testing.T) {
	committer := &lostAckStartCommitter{MemoryTreeCommitter: NewMemoryTreeCommitter()}
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodePayload(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	process, startErr := engine.Start(t.Context(), newChildTestDeployment(t), input)
	if process != nil {
		t.Fatal("unacknowledged start published a Process")
	}

	var evidence *StartError
	if !errors.As(startErr, &evidence) {
		t.Fatalf("Start error = %v, want *StartError", startErr)
	}
	if !evidence.RootID().Valid() || evidence.Sequence() != 1 || !evidence.Incarnation().Valid() || !evidence.Digest().Valid() {
		t.Fatalf("start evidence = %#v", evidence)
	}

	// The attempted head is reconcilable: the Host finds it by the reported root
	// and recognizes it by the reported digest and incarnation, trusting no
	// acknowledgment it never received.
	head, exists, err := committer.LoadTree(t.Context(), evidence.RootID())
	if err != nil || !exists {
		t.Fatalf("attempted head not found: exists=%v err=%v", exists, err)
	}
	if head.Digest() != evidence.Digest() || head.IncarnationID() != evidence.Incarnation() {
		t.Fatal("persisted head does not match the reported evidence")
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}
