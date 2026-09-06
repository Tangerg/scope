//go:build unix

package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFileOperationsRejectFIFOWithoutWaitingForIO(t *testing.T) {
	for _, operation := range []string{"read", "write", "edit"} {
		t.Run(operation, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "pipe")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			// A writer keeps a mistakenly opened reader blocked instead of at EOF.
			peer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			executor := mustLocalExecutor(t, directory)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				var operationErr error
				switch operation {
				case "read":
					_, operationErr = executor.Read(ctx, ReadInput{Path: "pipe"})
				case "write":
					_, operationErr = executor.Write(ctx, WriteRequest{Path: "pipe", Content: "new"})
				case "edit":
					_, operationErr = executor.Edit(ctx, EditRequest{Path: "pipe", OldString: "old", NewString: "new"})
				}
				finished <- operationErr
			}()
			select {
			case operationErr := <-finished:
				if operationErr == nil || !strings.Contains(operationErr.Error(), "unsupported file mode") {
					t.Fatalf("%s error = %v, want non-regular file rejection", operation, operationErr)
				}
			case <-ctx.Done():
				t.Fatal("file operation waited for FIFO data")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("FIFO was replaced: info = %v, error = %v", info, err)
			}
		})
	}
}
