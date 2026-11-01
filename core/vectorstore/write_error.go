package vectorstore

import "fmt"

// WriteError reports native acknowledgments received before an indexing operation
// failed. An acknowledgment is not a guarantee of search visibility. Unconfirmed
// IDs must not be replayed merely because the caller received an error.
type WriteError struct {
	// Acknowledged contains IDs for which the backend acknowledged every item
	// in its write group.
	Acknowledged []string
	// Rejected contains IDs for which the backend explicitly rejected the write.
	Rejected []string
	// Unconfirmed is the attempted group whose individual outcomes are unknown.
	Unconfirmed []string
	// AcknowledgedCount is a usable aggregate acknowledgment for Unconfirmed,
	// when available. It does not identify particular records in that group.
	AcknowledgedCount *int
	// Unattempted contains IDs in groups the adapter never submitted.
	Unattempted []string
	Err         error
}

func (w *WriteError) Error() string {
	return fmt.Sprintf("vectorstore: write: %v (acknowledged=%d, rejected=%d, unconfirmed=%d, unattempted=%d)",
		w.Err, len(w.Acknowledged), len(w.Rejected), len(w.Unconfirmed), len(w.Unattempted))
}

func (w *WriteError) Unwrap() error { return w.Err }
