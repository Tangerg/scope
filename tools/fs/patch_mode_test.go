package fs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestApplyPatchRejectsUnsupportedModesBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name  string
		patch string
	}{
		{
			name: "mode change with text hunk",
			patch: "diff --git a/existing b/existing\nold mode 100644\nnew mode 100755\n" +
				"--- a/existing\n+++ b/existing\n@@ -1 +1 @@\n-old\n+new\n",
		},
		{
			name: "mode change with rename",
			patch: "diff --git a/existing b/moved\nold mode 100644\nnew mode 100755\n" +
				"similarity index 100%\nrename from existing\nrename to moved\n",
		},
		{
			name: "executable creation",
			patch: "diff --git a/created b/created\nnew file mode 100755\n" +
				"--- /dev/null\n+++ b/created\n@@ -0,0 +1 @@\n+new\n",
		},
		{
			name: "symlink creation",
			patch: "diff --git a/created b/created\nnew file mode 120000\n" +
				"--- /dev/null\n+++ b/created\n@@ -0,0 +1 @@\n+target\n",
		},
		{
			name: "gitlink creation",
			patch: "diff --git a/created b/created\nnew file mode 160000\n" +
				"--- /dev/null\n+++ b/created\n@@ -0,0 +1 @@\n+Subproject commit 0123456789012345678901234567890123456789\n",
		},
		{
			name: "nonregular source mode",
			patch: "diff --git a/existing b/existing\nindex 1111111..2222222 120000\n" +
				"--- a/existing\n+++ b/existing\n@@ -1 +1 @@\n-old\n+new\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			original := filepath.Join(root, "existing")
			if err := os.WriteFile(original, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			executor := mustLocalExecutor(t, root)
			request := ApplyPatchRequest{Patch: "--- /dev/null\n+++ admitted\n@@ -0,0 +1 @@\n+first\n" + test.patch}
			paths, err := mustApplyPatchTool(t, executor).MutationPaths(mustJSON(t, request))
			if !errors.Is(err, ErrInvalidInput) || len(paths) != 0 {
				t.Errorf("MutationPaths = %v, %v; want unsupported mode rejection", paths, err)
			}
			result, err := executor.ApplyPatch(t.Context(), request)
			if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrMutationRejected) || len(result.Files) != 0 {
				t.Errorf("ApplyPatch = %+v, %v; want rejection before any file commits", result, err)
			}
			data, readErr := os.ReadFile(original)
			if readErr != nil || string(data) != "old\n" {
				t.Errorf("original after rejection = %q, %v", data, readErr)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 1 || entries[0].Name() != "existing" {
				t.Errorf("rejected patch changed directory: %v, %v", entries, readErr)
			}
		})
	}
}

func TestApplyPatchPreservesRegularFileModes(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "script")
	if err := os.WriteFile(original, []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(original, 0o755); err != nil {
		t.Fatal(err)
	}
	executor := mustLocalExecutor(t, root)
	request := ApplyPatchRequest{Patch: "diff --git a/created b/created\nnew file mode 100644\n" +
		"--- /dev/null\n+++ b/created\n@@ -0,0 +1 @@\n+created\n" +
		"diff --git a/script b/script\nindex 1111111..2222222 100755\n" +
		"--- a/script\n+++ b/script\n@@ -1 +1 @@\n-old\n+new\n"}
	result, err := executor.ApplyPatch(t.Context(), request)
	if err != nil || len(result.Files) != 2 || result.Hunks != 2 {
		t.Fatalf("ApplyPatch = %+v, %v", result, err)
	}
	for _, test := range []struct{ path, content string }{{"created", "created\n"}, {"script", "new\n"}} {
		data, readErr := os.ReadFile(filepath.Join(root, test.path))
		if readErr != nil || string(data) != test.content {
			t.Errorf("%s = %q, %v; want %q", test.path, data, readErr, test.content)
		}
	}
	info, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("existing executable mode changed to %o", info.Mode().Perm())
	}
}
