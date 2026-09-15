package fs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestApplyPatchRejectsAncestorEndpointsBeforeMutation(t *testing.T) {
	for _, paths := range [][2]string{{"parent", "parent/child"}, {"parent/child", "parent"}} {
		root := t.TempDir()
		patch := ""
		for _, path := range paths {
			patch += "--- /dev/null\n+++ " + path + "\n@@ -0,0 +1 @@\n+created\n"
		}
		out, err := mustLocalExecutor(t, root).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
		if err == nil || len(out.Files) != 0 {
			t.Fatalf("ancestor conflict returned %#v, %v", out, err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected patch changed filesystem: %v, %v", entries, err)
		}
	}
}

func readOnlyPatchDirectory(t *testing.T, root string) string {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires directory permission enforcement")
	}
	dir := filepath.Join(root, "locked")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, dir, "original", "old\n")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestApplyPatchCommitFailurePreservesAcknowledgedChanges(t *testing.T) {
	for _, asTool := range []bool{false, true} {
		root := t.TempDir()
		readOnlyPatchDirectory(t, root)
		executor := mustLocalExecutor(t, root)
		request := ApplyPatchRequest{Patch: "--- /dev/null\n+++ created\n@@ -0,0 +1 @@\n+first\n" +
			"--- locked/original\n+++ locked/original\n@@ -1 +1 @@\n-old\n+new\n"}
		var out ApplyPatchResponse
		var err error
		if asTool {
			executable, constructorErr := NewApplyPatchTool(executor)
			if constructorErr != nil {
				t.Fatal(constructorErr)
			}
			arguments, encodeErr := json.Marshal(request)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			_, err = invokeTestTool(t.Context(), executable, string(arguments))
			failure, found := errors.AsType[*tool.Failure](err)
			if !found {
				t.Fatalf("tool failure lost its output: %v", err)
			}
			if decodeErr := json.Unmarshal(failure.Output().Details, &out); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			text, _ := failure.Output().Text()
			if !strings.Contains(text, `"path":"created"`) {
				t.Fatalf("model-visible failure lost acknowledged files: %s", text)
			}
			err = failure.Cause()
		} else {
			out, err = executor.ApplyPatch(t.Context(), request)
		}
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("commit failure = %v, want permission error", err)
		}
		want := ApplyPatchResponse{Files: []PatchFileResponse{{Path: "created", Hunks: 1, Created: true}}, Hunks: 1}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("partial outcome = %#v, want %#v", out, want)
		}
		if content, readErr := os.ReadFile(filepath.Join(root, "created")); readErr != nil || string(content) != "first\n" {
			t.Fatalf("acknowledged file = %q, %v", content, readErr)
		}
		if content, readErr := os.ReadFile(filepath.Join(root, "locked/original")); readErr != nil || string(content) != "old\n" {
			t.Fatalf("uncommitted file = %q, %v", content, readErr)
		}
	}
}

func TestApplyPatchInterruptedMoveReportsCreatedDestination(t *testing.T) {
	root := t.TempDir()
	readOnlyPatchDirectory(t, root)
	out, err := mustLocalExecutor(t, root).ApplyPatch(t.Context(), ApplyPatchRequest{
		Patch: movePatch("locked/original", "destination", ""),
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("source removal = %v, want permission error", err)
	}
	want := ApplyPatchResponse{Files: []PatchFileResponse{{Path: "destination", Created: true}}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("interrupted move = %#v, want %#v", out, want)
	}
	for _, path := range []string{"locked/original", "destination"} {
		if content, readErr := os.ReadFile(filepath.Join(root, path)); readErr != nil || string(content) != "old\n" {
			t.Fatalf("%s = %q, %v", path, content, readErr)
		}
	}
}

func TestPatchPreparationDoesNotCreateDirectories(t *testing.T) {
	for _, suffix := range []string{
		"--- existing\n+++ existing\n@@ -1 +1 @@\n-stale\n+new\n",
		"--- missing\n+++ missing\n@@ -1 +1 @@\n-old\n+new\n",
		"--- /dev/null\n+++ existing\n@@ -0,0 +1 @@\n+new\n",
	} {
		root := t.TempDir()
		writeTemp(t, root, "existing", "old\n")
		out, err := mustLocalExecutor(t, root).ApplyPatch(t.Context(), ApplyPatchRequest{
			Patch: "--- /dev/null\n+++ newdir/nested/file\n@@ -0,0 +1 @@\n+first\n" + suffix,
		})
		if err == nil || len(out.Files) != 0 {
			t.Fatalf("rejected patch: %+v, %v", out, err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 1 || entries[0].Name() != "existing" {
			t.Fatalf("preparation mutated directory: %v, %v", entries, err)
		}
		data, err := os.ReadFile(filepath.Join(root, "existing"))
		if err != nil || string(data) != "old\n" {
			t.Fatalf("preparation mutated file: %q, %v", data, err)
		}
	}
}

func TestCanceledMutationsDoNotCreateDirectories(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	root := t.TempDir()
	executor := mustLocalExecutor(t, root)
	if _, err := executor.Write(ctx, WriteRequest{Path: "write/nested/file", Content: "new"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := executor.ApplyPatch(ctx, ApplyPatchRequest{Patch: "--- /dev/null\n+++ patch/nested/file\n@@ -0,0 +1 @@\n+new\n"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled mutations created directories: %v, %v", entries, err)
	}
}
