package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalExecutorTreatsTildeAsALiteralPath(t *testing.T) {
	root := t.TempDir()
	literal := filepath.Join(root, "~")
	if err := os.Mkdir(literal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(literal, "value"), []byte("workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := mustLocalExecutor(t, root)
	for _, home := range []string{root, t.TempDir(), ""} {
		t.Setenv("HOME", home)
		result, err := executor.Read(t.Context(), ReadInput{Path: "~/value"})
		if err != nil || result.Content != "workspace" {
			t.Fatalf("literal path changed with environment: result=%#v error=%v", result, err)
		}
	}
}
