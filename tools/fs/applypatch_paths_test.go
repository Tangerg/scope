package fs

import (
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestApplyPatchMutationPathsMatchExecution(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(newline, func(t *testing.T) {
			root := t.TempDir()
			writeTemp(t, root, "modified", "old\n")
			writeTemp(t, root, "deleted", "gone\n")
			writeTemp(t, root, "source", "moved\n")
			patch := "diff --git a/created b/created\nnew file mode 100644\n--- /dev/null\n+++ b/created\n@@ -0,0 +1 @@\n+new\n" +
				"--- a/modified\n+++ b/modified\n@@ -1 +1 @@\n-old\n+changed\n" +
				"diff --git a/deleted b/deleted\ndeleted file mode 100644\n--- a/deleted\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n" +
				movePatch("source", "destination", "")
			request := ApplyPatchRequest{Patch: strings.ReplaceAll(patch, "\n", newline)}
			executor := mustLocalExecutor(t, root)
			executable := mustApplyPatchTool(t, executor)
			paths, err := executable.MutationPaths(mustJSON(t, request))
			wantPaths := []string{"created", "deleted", "destination", "modified", "source"}
			if err != nil || !slices.Equal(paths, wantPaths) {
				t.Fatalf("MutationPaths = %v, %v; want %v", paths, err, wantPaths)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 3 {
				t.Fatalf("query changed directory: %v, %v", entries, err)
			}
			response, err := executor.ApplyPatch(t.Context(), request)
			want := ApplyPatchResponse{Files: []PatchFileResponse{
				{Path: "created", Hunks: 1, Created: true},
				{Path: "modified", Hunks: 1},
				{Path: "deleted", Hunks: 1, Deleted: true},
				{Path: "destination", MovedFrom: "source"},
			}, Hunks: 3}
			if err != nil || !reflect.DeepEqual(response, want) {
				t.Fatalf("ApplyPatch = %#v, %v; want %#v", response, err, want)
			}
			for path, want := range map[string]string{"created": "new\n", "modified": "changed\n", "destination": "moved\n"} {
				body, err := os.ReadFile(filepath.Join(root, path))
				if err != nil || string(body) != want {
					t.Fatalf("%s = %q, %v; want %q", path, body, err, want)
				}
			}
			for _, path := range []string{"deleted", "source"} {
				if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
					t.Fatalf("%s remains after patch: %v", path, err)
				}
			}
		})
	}
}

func TestApplyPatchMutationPathsNeedNoFilesystem(t *testing.T) {
	executor := mustLocalExecutor(t, t.TempDir())
	executable := mustApplyPatchTool(t, executor)
	if err := executor.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		patch string
		want  []string
	}{
		{"cleaned paths", "--- a/missing/../file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n", []string{"file"}},
		{"create", "--- /dev/null\n+++ b/missing/file\n@@ -0,0 +1 @@\n+new\n", []string{filepath.FromSlash("missing/file")}},
		{"delete", "--- a/missing\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n", []string{"missing"}},
		{"rename with hunks", movePatch("before", "after", "@@ -1 +1 @@\n-old\n+new\n"), []string{"after", "before"}},
		{"quoted path", "--- /dev/null\n+++ \"b/space name\"\n@@ -0,0 +1 @@\n+new\n", []string{"space name"}},
		{"absolute path", "--- /dev/null\n+++ /outside/file\n@@ -0,0 +1 @@\n+new\n", []string{filepath.FromSlash("/outside/file")}},
		{"repeated endpoint", "--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n--- a/file\n+++ b/file\n@@ -1 +1 @@\n-new\n+next\n", []string{"file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := executable.MutationPaths(mustJSON(t, ApplyPatchRequest{Patch: tc.patch}))
			if err != nil || !slices.Equal(paths, tc.want) {
				t.Fatalf("MutationPaths = %v, %v; want %v", paths, err, tc.want)
			}
		})
	}
}

func TestApplyPatchMutationPathsRejectInvalidPatches(t *testing.T) {
	executor := mustLocalExecutor(t, t.TempDir())
	executable := mustApplyPatchTool(t, executor)
	for _, patch := range []string{
		"", "not a patch",
		"--- a/file\n+++ b/file\n@@ -1,2 +1,2 @@\n-old\n+new\n",
		"--- a/file\n+++ b/file\n@@ --1,1 +1,1 @@\n-old\n+new\n",
		"diff --git a/file b/file\nold mode 100644\nnew mode 100755\n",
		"diff --git a/file b/file\nBinary files a/file and b/file differ\n",
		"diff --git a/source b/destination\nsimilarity index 100%\ncopy from source\ncopy to destination\n",
		"--- /dev/null\n+++ b/.\n@@ -0,0 +1 @@\n+new\n",
	} {
		request := ApplyPatchRequest{Patch: patch}
		paths, queryErr := executable.MutationPaths(mustJSON(t, request))
		response, executionErr := executor.ApplyPatch(t.Context(), request)
		if queryErr == nil || paths != nil || executionErr == nil || len(response.Files) != 0 {
			t.Fatalf("invalid patch %q: query %v, %v; execution %#v, %v", patch, paths, queryErr, response, executionErr)
		}
		if queryErr.Error() != executionErr.Error() {
			t.Fatalf("validation differs: query %v; execution %v", queryErr, executionErr)
		}
	}
	valid := string(mustJSON(t, ApplyPatchRequest{Patch: movePatch("source", "destination", "")}))
	for _, arguments := range []string{
		`{`, `null`, `{}`, `{"patch":1}`, `{"patch":null}`,
		strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
		strings.TrimSuffix(valid, "}") + `,"patch":"other"}`,
		strings.Replace(valid, `"patch":`, `"Patch":`, 1),
	} {
		if paths, err := executable.MutationPaths([]byte(arguments)); err == nil || paths != nil {
			t.Fatalf("invalid arguments %s returned %v, %v", arguments, paths, err)
		}
	}
}

func TestApplyPatchPublishedExamples(t *testing.T) {
	root := t.TempDir()
	executable := mustApplyPatchTool(t, mustLocalExecutor(t, root))
	examples := regexp.MustCompile("(?s)```diff\\n(.*?)```").FindAllStringSubmatch(executable.Definition().Description, -1)
	if len(examples) != 3 {
		t.Fatal("expected create, modify, and delete examples")
	}
	for index, example := range examples {
		arguments, err := jsonv2.Marshal(ApplyPatchRequest{Patch: example[1]})
		if err != nil {
			t.Fatal(err)
		}
		if paths, err := executable.MutationPaths(arguments); err != nil || !slices.Equal(paths, []string{"notes.txt"}) {
			t.Fatalf("example %d paths = %v, %v", index, paths, err)
		}
		if _, err := invokeTestTool(t.Context(), executable, string(arguments)); err != nil {
			t.Fatalf("example %d: %v", index, err)
		}
		if index < 2 {
			body, err := os.ReadFile(filepath.Join(root, "notes.txt"))
			if want := []string{"first\n", "second\n"}[index]; err != nil || string(body) != want {
				t.Fatalf("example %d = %q, %v; want %q", index, body, err, want)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete example left file: %v", err)
	}
}
