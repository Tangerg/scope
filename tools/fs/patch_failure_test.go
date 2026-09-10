package fs

import (
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
