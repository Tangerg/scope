package fs_test

import (
	"os"
	"path/filepath"
	"testing"

	toolfs "github.com/Tangerg/scope/tools/fs"
)

func TestLocalExecutorSharesHostAuthorityAcrossRootReplacement(t *testing.T) {
	for _, replaceBeforeConstruction := range []bool{true, false} {
		name := "after construction"
		if replaceBeforeConstruction {
			name = "before construction"
		}
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			original, moved := filepath.Join(parent, "workspace"), filepath.Join(parent, "moved")
			if err := os.Mkdir(original, 0o700); err != nil {
				t.Fatal(err)
			}
			root, openErr := os.OpenRoot(original)
			if openErr != nil {
				t.Fatal(openErr)
			}
			t.Cleanup(func() {
				if err := root.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := root.WriteFile("file", []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			replace := func() {
				t.Helper()
				if err := os.Rename(original, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(original, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(original, "file"), []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if replaceBeforeConstruction {
				replace()
			}
			executor, constructErr := toolfs.NewLocalExecutor(root)
			if constructErr != nil {
				t.Fatal(constructErr)
			}
			t.Cleanup(func() {
				if err := executor.Close(); err != nil {
					t.Error(err)
				}
			})
			if !replaceBeforeConstruction {
				replace()
			}
			observed, err := root.ReadFile("file")
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"file", filepath.Join(original, "file")} {
				read, err := executor.Read(t.Context(), toolfs.ReadInput{Path: path})
				if err != nil || read.Content != string(observed) {
					t.Fatalf("host observed %q, Scope read %q: %v", observed, read.Content, err)
				}
			}
			if _, err := executor.Edit(t.Context(), toolfs.EditRequest{Path: "file", OldString: "original", NewString: "edited"}); err != nil {
				t.Fatal(err)
			}
			if data, err := root.ReadFile("file"); err != nil || string(data) != "edited" {
				t.Fatalf("host view after edit = %q, %v", data, err)
			}
			if _, err := executor.ApplyPatch(t.Context(), toolfs.ApplyPatchRequest{Patch: "--- /dev/null\n+++ b/created\n@@ -0,0 +1 @@\n+patched\n"}); err != nil {
				t.Fatal(err)
			}
			if data, err := root.ReadFile("created"); err != nil || string(data) != "patched\n" {
				t.Fatalf("host view after patch = %q, %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(original, "file")); err != nil || string(data) != "replacement" {
				t.Fatalf("replacement directory changed: %q, %v", data, err)
			}
		})
	}
}
