package run

import (
	"context"
	"errors"
	"fmt"
)

// StartID identifies a caller's immutable submission within a Host-authorized
// scope. It is known before sending the request and is not a ProcessID.
type StartID struct {
	Scope string
	Key   string
}

func (s StartID) Valid() bool { return validIdentity(s.Scope) && validIdentity(s.Key) }

// StartReceipt is the durable association installed with the first tree head.
// The backend must retain it across subsequent commits and writer activations.
type StartReceipt struct {
	ID     StartID
	Intent Digest
	RootID ProcessID
}

func (s StartReceipt) Valid() bool { return s.ID.Valid() && s.Intent.Valid() && s.RootID.Valid() }

func (s StartReceipt) loadHead(ctx context.Context, reader HeadReader) (TreeSnapshot, error) {
	return invokeCallback("HeadReader.LoadTree", func() (TreeSnapshot, error) {
		head, found, err := reader.LoadTree(ctx, s.RootID)
		if err != nil {
			return TreeSnapshot{}, err
		}
		if !found || !head.Valid() || head.RootID() != s.RootID {
			return TreeSnapshot{}, errors.New("run: accepted start has no matching acknowledged head")
		}
		return head, nil
	})
}

// Store admits new trees and reads their original acceptance. Initial head,
// commit deduplication and scoped StartReceipt are one atomic write.
type Store interface {
	TreeCommitter
	StartReader
}

// StartReader reads the original acceptance, never the current head or writer.
type StartReader interface {
	LookupStart(ctx context.Context, id StartID) (receipt StartReceipt, found bool, err error)
}

// StartAcceptedError carries proof of acceptance when this Runtime cannot
// establish that its local activation owns the acknowledged head.
type StartAcceptedError struct{ Receipt StartReceipt }

func (s *StartAcceptedError) Error() string {
	return fmt.Sprintf("run: start %s/%s already accepted as %s", s.Receipt.ID.Scope, s.Receipt.ID.Key, s.Receipt.RootID)
}

// StartConflictError reports reuse of a caller's key for a different intent.
type StartConflictError struct{ Receipt StartReceipt }

func (s *StartConflictError) Error() string {
	return fmt.Sprintf("run: start %s/%s conflicts with accepted run %s", s.Receipt.ID.Scope, s.Receipt.ID.Key, s.Receipt.RootID)
}

var ErrInvalidStart = errors.New("run: invalid start identity or receipt")

func startIntent(ref ProgramRef, input Payload) Digest {
	return ComputeDigest([]byte(ref.Digest().String() + "\x00" + string(input.JSON())))
}

func newStartCheckpoint(snapshot TreeSnapshot, receipt StartReceipt) (TreeCheckpoint, error) {
	c := TreeCheckpoint{sequence: 1, cause: checkpointCauseCut, treeSnapshot: snapshot, start: receipt}
	if !c.Valid() {
		return TreeCheckpoint{}, ErrInvalidStart
	}
	return c, nil
}
