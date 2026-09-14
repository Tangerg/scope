package fs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLocalExecutorKeepsDirectoryAuthorityAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	original, moved := filepath.Join(parent, "workspace"), filepath.Join(parent, "moved")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "file"), []byte("authorized"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := mustLocalExecutor(t, original)
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "file"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"file", filepath.Join(original, "file")} {
		read, err := executor.Read(t.Context(), ReadInput{Path: path})
		if err != nil || read.Content != "authorized" {
			t.Fatalf("read rebound directory: %+v, %v", read, err)
		}
	}
	if _, err := executor.Write(t.Context(), WriteRequest{Path: "file", Content: "updated"}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{filepath.Join(moved, "file"): "updated", filepath.Join(original, "file"): "replacement"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestLocalExecutorCloseEndsNewAuthorityAcquisition(t *testing.T) {
	executor, err := NewLocalExecutor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	closeExecutor := sync.OnceFunc(func() {
		if closeErr := executor.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	t.Cleanup(closeExecutor)
	acquired, err := executor.openRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Close()
	closeExecutor()
	if _, err := executor.openRoot(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed executor acquired authority: %v", err)
	}
	if _, err := acquired.Stat("."); err != nil {
		t.Fatalf("Close revoked an acquired operation: %v", err)
	}
}

func TestLocalExecutorRequiresExistingDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if executor, err := NewLocalExecutor(root); executor != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory = %v, %v", executor, err)
	}
}
