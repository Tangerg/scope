package agent

import "fmt"

// StartError reports that Start allocated a root identity and attempted its
// initial checkpoint, but the commit returned no acknowledgment. The attempted
// head is distinct from an acknowledged head: it may or may not be durable, and
// the Host owns reconciliation against its store using this evidence. Errors
// Start raises before it attempts the initial checkpoint are returned unwrapped,
// because no head was attempted. Unwrap exposes the underlying commit error.
type StartError struct {
	rootID      ProcessID
	incarnation TreeIncarnationID
	sequence    uint64
	digest      Digest
	err         error
}

// RootID is the identity Start assigned to the root Process and named in the
// attempted checkpoint.
func (e *StartError) RootID() ProcessID { return e.rootID }

// Incarnation is the writer identity that stamped the attempted checkpoint.
func (e *StartError) Incarnation() TreeIncarnationID { return e.incarnation }

// Sequence is the attempted checkpoint's sequence; a root's first head is 1.
func (e *StartError) Sequence() uint64 { return e.sequence }

// Digest is the content digest of the attempted head, so a Host can recognize it
// among persisted checkpoints without trusting any acknowledgment.
func (e *StartError) Digest() Digest { return e.digest }

func (e *StartError) Unwrap() error { return e.err }

func (e *StartError) Error() string {
	return fmt.Sprintf(
		"agent: start commit unacknowledged for root %s (incarnation %s, sequence %d, digest %s): %v",
		e.rootID, e.incarnation, e.sequence, e.digest, e.err,
	)
}
