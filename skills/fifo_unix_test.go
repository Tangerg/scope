//go:build unix

package skills

import (
	"context"
	"errors"
	"fmt"
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

func TestDirectoryMetadataRejectsFIFO(t *testing.T) {
	if os.Getenv("SCOPE_TEST_METADATA_FIFO_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestDirectoryMetadataRejectsFIFO$")
		command.Env = append(os.Environ(), "SCOPE_TEST_METADATA_FIFO_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("metadata FIFO check failed: %v\n%s", err, output)
		}
		return
	}

	for _, writerConnected := range []bool{false, true} {
		for _, operation := range []string{"load", "lookup", "list"} {
			t.Run(fmt.Sprintf("%s/writer=%t", operation, writerConnected), func(t *testing.T) {
				root := t.TempDir()
				directory := writeSkill(t, root, "fifo-skill")
				path := filepath.Join(directory, SkillFile)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				// A held reader allows a writer to connect before the repository opens
				// the pipe, without making the test depend on scheduling.
				reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				var writer *os.File
				if writerConnected {
					writer, err = os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer writer.Close()
				}
				repository, err := NewDirectoryRepository(root, RepositoryConfig{})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					var err error
					switch operation {
					case "load":
						_, err = repository.Load(ctx, "fifo-skill")
					case "lookup":
						_, err = repository.Lookup(ctx, "fifo-skill")
					case "list":
						var summaries []Summary
						summaries, err = repository.List(ctx)
						if len(summaries) != 0 {
							err = fmt.Errorf("invalid skill listed: %v", summaries)
						}
					}
					done <- err
				}()
				select {
				case err := <-done:
					if operation == "list" {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					if !errors.Is(err, ErrInvalidSkill) {
						t.Fatalf("metadata error = %v", err)
					}
				case <-time.After(time.Second):
					if writer != nil {
						_ = writer.Close()
					}
					<-done
					t.Fatal("metadata read blocked beyond cancellation")
				}
			})
		}
	}
}
