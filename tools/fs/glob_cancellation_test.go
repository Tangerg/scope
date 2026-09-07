package fs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	filesystem "github.com/Tangerg/scope/tools/fs"
)

func TestLocalGlobRejectsCancellationWithoutMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "present.txt"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor, err := filesystem.NewLocalExecutor(root)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Time{})
	defer stop()
	for _, contextCase := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "canceled", ctx: canceled, want: context.Canceled},
		{name: "expired", ctx: expired, want: context.DeadlineExceeded},
	} {
		for _, pattern := range []string{"**/*.missing", "missing.txt", "present.txt"} {
			t.Run(contextCase.name+"/"+pattern, func(t *testing.T) {
				response, err := executor.Glob(contextCase.ctx, filesystem.GlobRequest{Pattern: pattern})
				if !errors.Is(err, contextCase.want) || response.Paths != nil || response.Truncated {
					t.Fatalf("canceled glob = %#v, error %v; want empty result and %v", response, err, contextCase.want)
				}
			})
		}
	}
}
