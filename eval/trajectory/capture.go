package trajectory

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
)

var ErrIncompleteRecording = errors.New("eval/trajectory: incomplete recording")

type Source interface {
	ID() run.ProcessID
	Inspect(context.Context) (run.TreeInspection, error)
	Observe(context.Context, run.LSN) iter.Seq2[run.Entry, error]
}

// Capture uses the committed head as a fence. It never waits for future work or
// consumes the source, so unresolved effects and repeated exports remain useful.
// Await or Join before Capture when a terminal trajectory is required.
func Capture(ctx context.Context, source Source, coverage *Coverage) (Trajectory, error) {
	if lo.IsNil(source) || !source.ID().Valid() || source.ID().Depth() != 0 {
		return Trajectory{}, fmt.Errorf("%w: root log source is required", ErrIncompleteRecording)
	}
	head, err := source.Inspect(ctx)
	if err != nil {
		return Trajectory{}, err
	}
	if head.Root != source.ID() || head.Head == 0 {
		return Trajectory{}, ErrIncompleteRecording
	}
	log := run.Log{Root: head.Root}
	for entry, err := range source.Observe(ctx, 0) {
		if err != nil {
			return Trajectory{}, err
		}
		if entry.LSN > head.Head {
			return Trajectory{}, fmt.Errorf("%w: source skipped the captured head", ErrIncompleteRecording)
		}
		log.Entries = append(log.Entries, entry)
		if entry.LSN == head.Head {
			return New(Config{Log: log, Coverage: coverage})
		}
	}
	return Trajectory{}, fmt.Errorf("%w: source ended before the captured head", ErrIncompleteRecording)
}
