//go:build unix

package skills

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDirectoryResourceRejectsFIFOWithoutBlocking(t *testing.T) {
	if os.Getenv("SCOPE_TEST_FIFO_CHILD") == "1" {
		root := t.TempDir()
		directory := writeSkill(t, root, "fifo-skill")
		if err := syscall.Mkfifo(filepath.Join(directory, "pipe"), 0o600); err != nil {
			t.Fatal(err)
		}
		repository, err := NewDirectoryRepository(root, RepositoryConfig{})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = ReadResource(t.Context(), repository, "fifo-skill", "pipe", DefaultMaxResourceBytes)
		if !errors.Is(err, ErrResourceNotRegular) {
			t.Fatalf("FIFO error = %v", err)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestDirectoryResourceRejectsFIFOWithoutBlocking$")
	command.Env = append(os.Environ(), "SCOPE_TEST_FIFO_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("FIFO open did not terminate successfully: %v\n%s", err, output)
	}
}
