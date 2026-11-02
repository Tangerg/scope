package run

import "context"

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
	receipt, ok, err := id.lookup(ctx, s.starts)
	if err != nil || !ok {
		return TreeSnapshot{}, false, err
	}
	head, err := receipt.loadHead(ctx, s.heads)
	return head, err == nil, err
}
