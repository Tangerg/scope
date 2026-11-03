package tool_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestProgressReachesOnlyTheObserverItsDriverInstalled(t *testing.T) {
	if err := tool.ReportProgress(t.Context(), tool.Progress{Completed: 1}); err != nil {
		t.Fatalf("progress without an observer failed: %v", err)
	}
	var observed []tool.Progress
	ctx := tool.WithProgressReporter(t.Context(), tool.ProgressReporterFunc(func(_ context.Context, progress tool.Progress) error {
		observed = append(observed, progress)
		return nil
	}))
	if err := tool.ReportProgress(ctx, tool.Progress{Completed: 1, Total: 2, Message: "half"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []tool.Progress{{Completed: -1}, {Completed: math.NaN()}, {Completed: 3, Total: 2}, {Total: math.Inf(1)}, {Message: "\xff"}} {
		if err := tool.ReportProgress(ctx, invalid); !errors.Is(err, tool.ErrInvalidProgress) {
			t.Fatalf("invalid progress %+v admitted: %v", invalid, err)
		}
	}
	if len(observed) != 1 || observed[0] != (tool.Progress{Completed: 1, Total: 2, Message: "half"}) {
		t.Fatalf("observed=%+v", observed)
	}
}
