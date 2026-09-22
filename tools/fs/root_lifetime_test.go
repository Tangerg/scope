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
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	executor, err := NewLocalExecutor(root)
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

func TestLocalExecutorRequiresOpenDirectory(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if executor, err := NewLocalExecutor(root); executor != nil || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed directory = %v, %v", executor, err)
	}
}

func TestLocalExecutorRequiresAbsoluteRootName(t *testing.T) {
	t.Chdir(t.TempDir())
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if executor, err := NewLocalExecutor(root); executor != nil || !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("relative root = %v, %v", executor, err)
	}
	if _, err := root.Stat("."); err != nil {
		t.Fatalf("failed construction closed the host root: %v", err)
	}
}

func TestLocalExecutorAndHostOwnIndependentLifetimes(t *testing.T) {
	for _, closeHost := range []bool{true, false} {
		name := "executor closes first"
		if closeHost {
			name = "host closes first"
		}
		t.Run(name, func(t *testing.T) {
			root, openErr := os.OpenRoot(t.TempDir())
			if openErr != nil {
				t.Fatal(openErr)
			}
			closeRoot := sync.OnceValue(root.Close)
			t.Cleanup(func() {
				if err := closeRoot(); err != nil {
					t.Error(err)
				}
			})
			if err := root.WriteFile("file", []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			executor, err := NewLocalExecutor(root)
			if err != nil {
				t.Fatal(err)
			}
			closeExecutor := sync.OnceValue(executor.Close)
			t.Cleanup(func() {
				if err := closeExecutor(); err != nil {
					t.Error(err)
				}
			})
			if closeHost {
				if err := closeRoot(); err != nil {
					t.Fatal(err)
				}
				read, err := executor.Read(t.Context(), ReadInput{Path: "file"})
				if err != nil || read.Content != "original" {
					t.Fatalf("host close revoked executor authority: %+v, %v", read, err)
				}
			} else {
				if err := closeExecutor(); err != nil {
					t.Fatal(err)
				}
				if data, err := root.ReadFile("file"); err != nil || string(data) != "original" {
					t.Fatalf("executor close revoked host authority: %q, %v", data, err)
				}
			}
		})
	}
}
