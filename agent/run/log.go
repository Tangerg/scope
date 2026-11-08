package run

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
)

var (
	ErrInvalidLog           = errors.New("run: invalid log")
	ErrCommitConflict       = errors.New("run: log position conflict")
	ErrEpochConflict        = errors.New("run: writer epoch conflict")
	ErrNondeterministicStep = errors.New("run: replayed step differs from committed transition")
)

// Store owns one append-only log per root tree. Recovery and history are
// separate reads so neither pays for the other's range: Load skips the prefix
// covered by the newest checkpoint, and Read is incremental.
type Store interface {
	// Append is the only operation that advances a tree. It must atomically
	// compare the writer Epoch and the head position before extending the log,
	// and acknowledge a byte-identical retry of an applied batch.
	Append(ctx context.Context, batch Batch) error
	// Load returns the recovery segment: the newest Checkpoint entry and every
	// entry after it, or the whole log when it holds no Checkpoint.
	Load(ctx context.Context, root ProcessID) (Log, bool, error)
	// Read yields the committed entries after position in order. It reads one
	// consistent snapshot and ends at that snapshot's head.
	Read(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error]
}

// Log is a contiguous segment of one tree's log that begins at the tree's
// Start or at a Checkpoint, so it can be folded without what precedes it.
type Log struct {
	Root    ProcessID `json:"root"`
	Entries []Entry   `json:"entries"`
}

// Complete reports whether the log begins at the tree's Start rather than at a
// Checkpoint, which history projections require.
func (l Log) Complete() bool { return len(l.Entries) > 0 && l.Entries[0].LSN == 1 }

func (l Log) Head() LSN {
	if len(l.Entries) == 0 {
		return 0
	}
	return l.Entries[len(l.Entries)-1].LSN
}

func (l Log) Epoch() Epoch {
	if len(l.Entries) == 0 {
		return 0
	}
	return l.Entries[len(l.Entries)-1].Epoch
}

func (l Log) rootStarted() (Started, error) {
	if len(l.Entries) == 0 {
		return Started{}, ErrInvalidLog
	}
	switch first := l.Entries[0].Record.(type) {
	case Started:
		if first.Process == l.Root {
			return first, nil
		}
	case Checkpoint:
		for _, process := range first.Processes {
			if process.Started.Process == l.Root {
				return process.Started, nil
			}
		}
	}
	return Started{}, ErrInvalidLog
}

// base is the state a Log starts from: the tree its leading Checkpoint
// materialized, or an empty tree when the Log begins with the tree's Start.
func (l Log) base() (*treeState, []Entry, error) {
	if err := l.validateEnvelope(); err != nil {
		return nil, nil, err
	}
	first := l.Entries[0]
	if checkpoint, ok := first.Record.(Checkpoint); ok {
		state, err := checkpoint.tree(l.Root, first)
		return state, l.Entries[1:], err
	}
	return newTreeState(l.Root), l.Entries, nil
}

// validateEnvelope checks the coordinates every segment shares: contiguous
// positions, monotonic epochs, one root, and resolved record limits. A segment
// begins either at the tree's Start or at a Checkpoint.
func (l Log) validateEnvelope() error {
	if !l.Root.Valid() || l.Root.Depth() != 0 || len(l.Entries) == 0 {
		return ErrInvalidLog
	}
	limits, epoch, err := l.origin()
	if err != nil {
		return err
	}
	first := l.Entries[0].LSN
	for index, entry := range l.Entries {
		if entry.LSN != first+LSN(index) {
			return ErrInvalidLog
		}
		if err := entry.ValidateFor(l.Root); err != nil {
			return err
		}
		if err := limits.admitRecord(entry.Record); err != nil {
			return err
		}
		if activation, ok := entry.Record.(Activated); ok {
			if epoch == Epoch(math.MaxUint64) || activation.Epoch != epoch+1 {
				return ErrInvalidLog
			}
			epoch = activation.Epoch
		}
		if entry.Epoch != epoch {
			return ErrInvalidLog
		}
	}
	return nil
}

func (l Log) origin() (TreeLimits, Epoch, error) {
	started, err := l.rootStarted()
	if err != nil {
		return TreeLimits{}, 0, err
	}
	first := l.Entries[0]
	if _, checkpoint := first.Record.(Checkpoint); checkpoint {
		if first.Epoch == 0 {
			return TreeLimits{}, 0, ErrInvalidLog
		}
		return started.Limits, first.Epoch, nil
	}
	if first.LSN != 1 || len(l.Entries) < 2 {
		return TreeLimits{}, 0, ErrInvalidLog
	}
	if activation, ok := l.Entries[1].Record.(Activated); !ok || activation.Epoch != 1 {
		return TreeLimits{}, 0, ErrInvalidLog
	}
	return started.Limits, 0, nil
}

func (l Log) Clone() (Log, error) {
	if err := l.Validate(); err != nil {
		return Log{}, err
	}
	entries, err := cloneEntries(l.Entries)
	if err != nil {
		return Log{}, err
	}
	return Log{Root: l.Root, Entries: entries}, nil
}

func (l Log) Inspect() (TreeInspection, error) {
	state, err := projectLog(l)
	if err != nil {
		return TreeInspection{}, err
	}
	return state.inspect(), nil
}

// Usage requires the complete prefix because checkpoints omit reaped
// processes. A recovery cut cannot certify the tree's historical total.
func (l Log) Usage() (Usage, error) {
	if err := l.Validate(); err != nil {
		return Usage{}, err
	}
	if !l.Complete() {
		return Usage{}, fmt.Errorf("%w: usage requires history from the tree start", ErrInvalidLog)
	}
	var usage Usage
	for _, entry := range l.Entries {
		_, increment := UsageFromRecord(entry.Record)
		var err error
		usage, err = usage.add(increment)
		if err != nil {
			return Usage{}, err
		}
	}
	return usage, nil
}

func (l Log) Validate() error { _, err := projectLog(l); return err }
