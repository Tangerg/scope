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
func (s *StartError) RootID() ProcessID { return s.rootID }

// Incarnation is the writer identity that stamped the attempted checkpoint.
func (s *StartError) Incarnation() TreeIncarnationID { return s.incarnation }

// Sequence is the attempted checkpoint's sequence; a root's first head is 1.
func (s *StartError) Sequence() uint64 { return s.sequence }

// Digest is the content digest of the attempted head, so a Host can recognize it
// among persisted checkpoints without trusting any acknowledgment.
func (s *StartError) Digest() Digest { return s.digest }

func (s *StartError) Unwrap() error { return s.err }

func (s *StartError) Error() string {
	return fmt.Sprintf(
		"agent: start commit unacknowledged for root %s (incarnation %s, sequence %d, digest %s): %v",
		s.rootID, s.incarnation, s.sequence, s.digest, s.err,
	)
}
