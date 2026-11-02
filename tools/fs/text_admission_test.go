package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAndEditRejectTheSameInvalidSourceWithoutMutation(t *testing.T) {
	const longPrefixBytes = 16 << 10
	for _, source := range []string{"old\xff\n", "old" + strings.Repeat("a", longPrefixBytes) + "\x00\n"} {
		root := t.TempDir()
		path := writeTemp(t, root, "source.txt", source)
		executor := mustLocalExecutor(t, root)
		if _, err := executor.Read(t.Context(), ReadInput{Path: path}); !errors.Is(err, ErrBinaryFile) {
			t.Fatalf("Read invalid source = %v", err)
		}
		if _, err := executor.Edit(t.Context(), EditRequest{Path: path, OldString: "old", NewString: "new"}); !errors.Is(err, ErrBinaryFile) || !errors.Is(err, ErrMutationRejected) {
			t.Fatalf("Edit invalid source = %v", err)
		}
		if actual, err := os.ReadFile(path); err != nil || string(actual) != source {
			t.Fatalf("rejected edit changed source: %q, %v", actual, err)
		}
	}
}

func TestWriteAndEditRejectInvalidUTF8BeforeMutation(t *testing.T) {
	for _, operation := range []string{"write", "edit"} {
		t.Run(operation, func(t *testing.T) {
			const original = "original"
			root := t.TempDir()
			path := writeTemp(t, root, "source.txt", original)
			executor := mustLocalExecutor(t, root)
			var err error
			if operation == "write" {
				_, err = executor.Write(t.Context(), WriteRequest{Path: path, Content: "\xff"})
			} else {
				_, err = executor.Edit(t.Context(), EditRequest{Path: path, OldString: original, NewString: "\xff"})
			}
			if !errors.Is(err, ErrBinaryFile) || !errors.Is(err, ErrMutationRejected) {
				t.Fatalf("invalid UTF-8 candidate = %v", err)
			}
			if actual, err := os.ReadFile(path); err != nil || string(actual) != original {
				t.Fatalf("rejected mutation changed source: %q, %v", actual, err)
			}
		})
	}
}

func TestApplyPatchRejectsInvalidUTF8BeforeAnyFileCommits(t *testing.T) {
	root := t.TempDir()
	patch := "--- /dev/null\n+++ first.txt\n@@ -0,0 +1 @@\n+legal\n" +
		"--- /dev/null\n+++ second.txt\n@@ -0,0 +1 @@\n+bad\xff\n"
	result, err := mustLocalExecutor(t, root).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if !errors.Is(err, ErrBinaryFile) || !errors.Is(err, ErrMutationRejected) || len(result.Files) != 0 {
		t.Fatalf("invalid UTF-8 patch = %+v, %v", result, err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected patch created %s: %v", name, err)
		}
	}
}
