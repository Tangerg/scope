package fs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMutationDirectoryAliasesShareOwnership(t *testing.T) {
	root := t.TempDir()
	if checkErr := os.Mkdir(filepath.Join(root, "real"), 0o700); checkErr != nil {
		t.Fatal(checkErr)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Skip(err)
	}
	file := filepath.Join(root, "real", "a.txt")
	if checkErr := os.WriteFile(file, []byte("left=0\nright=0\n"), 0o600); checkErr != nil {
		t.Fatal(checkErr)
	}
	executor := mustLocalExecutor(t, root)
	patch := "--- real/a.txt\n+++ real/a.txt\n@@ -1,2 +1,2 @@\n-left=0\n+left=1\n right=0\n" +
		"--- alias/a.txt\n+++ alias/a.txt\n@@ -1,2 +1,2 @@\n left=0\n-right=0\n+right=1\n"
	result, err := executor.ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
	if err == nil || !strings.Contains(err.Error(), "duplicate target") || len(result.Files) != 0 {
		t.Fatalf("alias patch = %+v, %v", result, err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "left=0\nright=0\n" {
		t.Fatalf("rejected patch changed file: %q, %v", data, err)
	}
	var group sync.WaitGroup
	for _, edit := range []EditRequest{{Path: "real/a.txt", OldString: "left=0", NewString: "left=1"}, {Path: "alias/a.txt", OldString: "right=0", NewString: "right=1"}} {
		group.Go(func() {
			if _, checkErr := executor.Edit(t.Context(), edit); checkErr != nil {
				t.Error(checkErr)
			}
		})
	}
	group.Wait()
	data, err = os.ReadFile(file)
	if err != nil || string(data) != "left=1\nright=1\n" {
		t.Fatalf("concurrent alias edits lost updates: %q, %v", data, err)
	}
}

func TestMutationRetainsParentAcrossDirectoryReplacement(t *testing.T) {
	path := t.TempDir()
	if checkErr := os.Mkdir(filepath.Join(path, "selected"), 0o700); checkErr != nil {
		t.Fatal(checkErr)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	target, err := openMutationTarget(root, "selected/file", false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.parent.Close()
	if checkErr := root.Rename("selected", "original"); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := root.Mkdir("selected", 0o700); checkErr != nil {
		t.Fatal(checkErr)
	}
	prepared := preparedPatch{target: target, data: []byte("new"), mode: new(os.FileMode(0o600)), result: PatchFileResponse{Path: target.path, Created: true}}
	if _, checkErr := prepared.commit(t.Context()); checkErr != nil {
		t.Fatal(checkErr)
	}
	data, err := root.ReadFile("original/file")
	if err != nil || string(data) != "new" {
		t.Fatalf("pinned target lost: %q, %v", data, err)
	}
	if _, err := root.Stat("selected/file"); !os.IsNotExist(err) {
		t.Fatalf("replacement directory was mutated: %v", err)
	}
}

func TestGrepConfinedSelectionPreservesFilters(t *testing.T) {
	skipWithoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "main.go", "first\nneedle\nlast\n")
	writeTemp(t, root, "other.txt", "needle\n")
	writeTemp(t, root, ".hidden.go", "needle\n")
	writeTemp(t, root, ".gitignore", "main.go\n")
	executor := mustLocalExecutor(t, root)
	for _, input := range []GrepInput{{Pattern: "NEEDLE", IgnoreCase: true, FileType: "go"}, {Pattern: "needle", Glob: "**/*.go"}, {Pattern: "first.*last", Multiline: true, FileType: "go"}} {
		result, err := executor.Grep(t.Context(), input)
		if err != nil || len(result.Lines) != 1 || result.Lines[0].Path != "main.go" {
			t.Fatalf("filtered result = %+v, %v", result, err)
		}
	}
	if _, err := executor.Grep(t.Context(), GrepInput{Pattern: "needle", FileType: "unknown-type"}); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func TestMissingAliasParentsAreValidatedBeforeMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Skip(err)
	}
	for _, paths := range [][2]string{{"real/new/file", "alias/new/file"}, {"real/new", "alias/new/file"}} {
		patch := ""
		for _, path := range paths {
			patch += "--- /dev/null\n+++ " + path + "\n@@ -0,0 +1 @@\n+new\n"
		}
		result, err := mustLocalExecutor(t, root).ApplyPatch(t.Context(), ApplyPatchRequest{Patch: patch})
		if err == nil || len(result.Files) != 0 {
			t.Fatalf("alias collision was admitted: %+v %v", result, err)
		}
		entries, err := os.ReadDir(filepath.Join(root, "real"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("alias validation mutated directory: %v %v", entries, err)
		}
	}
}
