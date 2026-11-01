package run

import (
	"context"
	"errors"
)

// HeadReader reads the latest acknowledged tree without admitting work or
// acquiring a writer. Stores may provide this capability independently.
type HeadReader interface {
	// LoadTree returns found=false for an unknown root.
	LoadTree(ctx context.Context, root ProcessID) (TreeSnapshot, bool, error)
}

// StartedTrees is the Runtime's read-only view of scoped durable acceptances.
type StartedTrees struct {
	starts StartReader
	heads  HeadReader
}

// Load resolves the original scoped receipt to the current acknowledged head.
// It never starts or recovers a process, even after a previous writer exits.
func (s *StartedTrees) Load(ctx context.Context, id StartID) (TreeSnapshot, bool, error) {
	ctx = RequireContext(ctx)
	if !id.Valid() {
		return TreeSnapshot{}, false, ErrInvalidStart
	}
	receipt, ok, err := s.starts.LookupStart(ctx, id)
	if err != nil || !ok {
		return TreeSnapshot{}, false, err
	}
	if !receipt.Valid() || receipt.ID != id {
		return TreeSnapshot{}, false, ErrInvalidStart
	}
	head, found, err := s.heads.LoadTree(ctx, receipt.RootID)
	if err != nil {
		return TreeSnapshot{}, false, err
	}
	if !found || !head.Valid() || head.RootID() != receipt.RootID {
		return TreeSnapshot{}, false, errors.New("run: accepted start has no matching acknowledged head")
	}
	return head, true, nil
}

func answerRequest(wait WaitID, answer Payload) (SignalRequest, error) {
	return NewSignalRequest(SignalID{deriveIdentity("signal:answer:", "external-answer", wait.String())}, wait, answer.JSON())
}
