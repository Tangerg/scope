package run

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// countingCommitter proves a validation pass reaches no storage boundary.
type countingCommitter struct {
	Store
	calls atomic.Int64
}

func (c *countingCommitter) ActivateTree(ctx context.Context, activation TreeActivation) error {
	c.calls.Add(1)
	return c.Store.ActivateTree(ctx, activation)
}

func (c *countingCommitter) CommitEffect(ctx context.Context, boundary EffectBoundary) error {
	c.calls.Add(1)
	return c.Store.CommitEffect(ctx, boundary)
}

func (c *countingCommitter) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	c.calls.Add(1)
	return c.Store.CommitCheckpoint(ctx, checkpoint)
}

func restoreValidationFixture(t *testing.T, tree TreeSnapshot) (*Runtime, *countingCommitter, Program[Payload, Payload]) {
	t.Helper()
	committer := &countingCommitter{Store: newSnapshotTestCommitter(tree)}
	engine := controlValue(New(t.Context(), Config{Store: committer}))
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	return engine, committer, engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
}

// Validation answers a question about the snapshot. Fencing the previous writer
// or taking the captured identities would make asking it destructive.
func TestValidateRestorableTreeNeitherActivatesAWriterNorTakesIdentities(t *testing.T) {
	tree := completedTreeSnapshot(t)
	engine, committer, deployment := restoreValidationFixture(t, tree)
	for round := range 2 {
		if err := engine.ValidateRestorableTree(t.Context(), deployment, tree); err != nil {
			t.Fatalf("round %d rejected a restorable tree: %v", round, err)
		}
	}
	if calls := committer.calls.Load(); calls != 0 {
		t.Fatalf("validation reached the committer %d times", calls)
	}
	// The identities the snapshot captured must still be available afterwards.
	root, err := engine.Recover(t.Context(), deployment, tree)
	if err != nil {
		t.Fatalf("validation consumed the captured identities: %v", err)
	}
	if _, awaitErr := root.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	if err := root.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	mustCloseEngine(t, engine)
}

// Both entry points run one prepare pass, so a rejection reason cannot differ
// between asking and restoring.
func TestValidateRestorableTreeAgreesWithRestoreTree(t *testing.T) {
	tree := completedTreeSnapshot(t)
	cause := errors.New("restore rejected")
	for _, test := range []struct {
		name       string
		deployment func(*testing.T) Program[Payload, Payload]
		want       error
	}{
		{
			"unrestorable Execution",
			func(t *testing.T) Program[Payload, Payload] {
				definition := &restoreAdmissionDefinition{
					Definition: newEngineTestDefinition(t, "engine.effect", "effect"), err: cause,
				}
				return engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
			},
			cause,
		},
		{
			"root binding mismatch",
			func(t *testing.T) Program[Payload, Payload] {
				definition := newEngineTestDefinition(t, "engine.other", "effect")
				return engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
			},
			ErrInvalidTreeSnapshot,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, _, _ := restoreValidationFixture(t, tree)
			deployment := test.deployment(t)
			validateErr := engine.ValidateRestorableTree(t.Context(), deployment, tree)
			_, restoreErr := engine.Recover(t.Context(), deployment, tree)
			if !errors.Is(validateErr, test.want) || !errors.Is(restoreErr, test.want) {
				t.Fatalf("validate = %v, restore = %v; want %v", validateErr, restoreErr, test.want)
			}
			mustCloseEngine(t, engine)
		})
	}
}

// Validation reports what the snapshot supports, not what the Runtime will
// admit right now; a closed Runtime can still answer the question.
func TestValidateRestorableTreeSkipsAdmission(t *testing.T) {
	tree := completedTreeSnapshot(t)
	engine, _, deployment := restoreValidationFixture(t, tree)
	mustCloseEngine(t, engine)
	if err := engine.ValidateRestorableTree(t.Context(), deployment, tree); err != nil {
		t.Fatalf("a closed Engine refused to answer: %v", err)
	}
	if _, err := engine.Recover(t.Context(), deployment, tree); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed restore = %v, want %v", err, ErrClosed)
	}
}

func TestValidateRestorableTreeRejectsUnusableInput(t *testing.T) {
	tree := completedTreeSnapshot(t)
	engine, _, deployment := restoreValidationFixture(t, tree)
	defer mustCloseEngine(t, engine)
	var missing *Runtime
	if err := missing.ValidateRestorableTree(t.Context(), deployment, tree); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil Engine = %v", err)
	}
	if err := engine.ValidateRestorableTree(t.Context(), Program[Payload, Payload]{}, tree); !errors.Is(err, ErrInvalidProgram) {
		t.Fatalf("invalid Program = %v", err)
	}
	if err := engine.ValidateRestorableTree(t.Context(), deployment, TreeSnapshot{}); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("invalid snapshot = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := engine.ValidateRestorableTree(canceled, deployment, tree); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context = %v", err)
	}
}
