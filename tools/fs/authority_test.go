package fs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLocalExecutorRejectsLexicalPathEscapes(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "workspace")
	outsidePath := filepath.Join(parent, "outside")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsidePath, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outsidePath, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := mustLocalExecutor(t, rootPath)

	operations := []struct {
		name string
		call func() error
	}{
		{
			name: "read parent traversal",
			call: func() error {
				_, err := executor.Read(t.Context(), ReadInput{Path: "../outside/secret.txt"})
				return err
			},
		},
		{
			name: "write absolute outside",
			call: func() error {
				_, err := executor.Write(t.Context(), WriteRequest{Path: filepath.Join(outsidePath, "written.txt"), Content: "no"})
				return err
			},
		},
		{
			name: "edit absolute outside",
			call: func() error {
				_, err := executor.Edit(t.Context(), EditRequest{Path: secret, OldString: "secret", NewString: "leaked"})
				return err
			},
		},
		{
			name: "patch parent traversal",
			call: func() error {
				_, err := executor.ApplyPatch(t.Context(), ApplyPatchRequest{Patch: `--- /dev/null
+++ b/../outside/patched.txt
@@ -0,0 +1 @@
+no
`})
				return err
			},
		},
		{
			name: "glob root override",
			call: func() error {
				_, err := executor.Glob(t.Context(), GlobRequest{Pattern: "**/*", Path: outsidePath})
				return err
			},
		},
		{
			name: "glob pattern traversal",
			call: func() error {
				_, err := executor.Glob(t.Context(), GlobRequest{Pattern: "../outside/**/*"})
				return err
			},
		},
		{
			name: "grep root override",
			call: func() error {
				_, err := executor.Grep(t.Context(), GrepInput{Pattern: "secret", Path: outsidePath})
				return err
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); !errors.Is(err, ErrPathOutsideRoot) {
				t.Fatalf("error = %v, want ErrPathOutsideRoot", err)
			}
		})
	}

	if got, err := os.ReadFile(secret); err != nil || string(got) != "secret" {
		t.Fatalf("outside source changed: %q, %v", got, err)
	}
	for _, name := range []string{"written.txt", "patched.txt"} {
		if _, err := os.Stat(filepath.Join(outsidePath, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("outside target %q exists after refusal: %v", name, err)
		}
	}
}

func TestLocalExecutorRejectsSymlinkEscapes(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "workspace")
	outsidePath := filepath.Join(parent, "outside")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsidePath, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outsidePath, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	executor := mustLocalExecutor(t, rootPath)

	operations := []struct {
		name string
		call func() error
	}{
		{
			name: "read",
			call: func() error {
				_, err := executor.Read(t.Context(), ReadInput{Path: "escape/secret.txt"})
				return err
			},
		},
		{
			name: "write",
			call: func() error {
				_, err := executor.Write(t.Context(), WriteRequest{Path: "escape/written.txt", Content: "no"})
				return err
			},
		},
		{
			name: "edit",
			call: func() error {
				_, err := executor.Edit(t.Context(), EditRequest{Path: "escape/secret.txt", OldString: "secret", NewString: "leaked"})
				return err
			},
		},
		{
			name: "patch",
			call: func() error {
				_, err := executor.ApplyPatch(t.Context(), ApplyPatchRequest{Patch: `--- /dev/null
+++ b/escape/patched.txt
@@ -0,0 +1 @@
+no
`})
				return err
			},
		},
		{
			name: "glob",
			call: func() error {
				_, err := executor.Glob(t.Context(), GlobRequest{Pattern: "**/*", Path: "escape"})
				return err
			},
		},
		{
			name: "grep",
			call: func() error {
				_, err := executor.Grep(t.Context(), GrepInput{Pattern: "secret", Path: "escape"})
				return err
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); err == nil {
				t.Fatal("operation crossed an out-of-root symlink")
			}
		})
	}

	if got, err := os.ReadFile(secret); err != nil || string(got) != "secret" {
		t.Fatalf("outside source changed: %q, %v", got, err)
	}
	for _, name := range []string{"written.txt", "patched.txt"} {
		if _, err := os.Stat(filepath.Join(outsidePath, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("outside target %q exists after refusal: %v", name, err)
		}
	}
}

func TestLocalExecutorSearchSubtreeKeepsWorkspaceRelativePaths(t *testing.T) {
	skipWithoutRipgrep(t)
	rootPath := t.TempDir()
	writeTemp(t, rootPath, "src/one.txt", "needle\n")
	writeTemp(t, rootPath, "src/nested/two.txt", "needle\n")
	writeTemp(t, rootPath, "other.txt", "needle\n")
	executor := mustLocalExecutor(t, rootPath)

	glob, err := executor.Glob(t.Context(), GlobRequest{Pattern: "**/*.txt", Path: "src"})
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"src/nested/two.txt", "src/one.txt"}
	if !slices.Equal(glob.Paths, wantPaths) {
		t.Fatalf("glob paths = %v, want %v", glob.Paths, wantPaths)
	}

	grep, err := executor.Grep(t.Context(), GrepInput{
		Pattern: "needle", Path: "src", OutputMode: GrepOutputFilesWithMatches,
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(grep.Files)
	if !slices.Equal(grep.Files, wantPaths) {
		t.Fatalf("grep paths = %v, want %v", grep.Files, wantPaths)
	}
	for _, path := range grep.Files {
		if filepath.IsAbs(path) || strings.HasPrefix(path, "..") {
			t.Fatalf("grep returned non-workspace path %q", path)
		}
	}
}
