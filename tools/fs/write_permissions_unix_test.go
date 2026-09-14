//go:build unix

package fs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestWritePermissionsRespectCreationUmask(t *testing.T) {
	const helperMask = "SCOPE_TEST_WRITE_UMASK"
	if maskText := os.Getenv(helperMask); maskText != "" {
		mask, parseErr := strconv.ParseInt(maskText, 8, 32)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		syscall.Umask(int(mask))
		root := t.TempDir()
		executor := mustLocalExecutor(t, root)
		if _, err := executor.Write(t.Context(), WriteRequest{Path: "write/new", Content: "new\n"}); err != nil {
			t.Fatal(err)
		}
		if _, err := executor.ApplyPatch(t.Context(), ApplyPatchRequest{Patch: "--- /dev/null\n+++ patch/new\n@@ -0,0 +1 @@\n+new\n"}); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"write/new", "patch/new"} {
			info, err := os.Stat(filepath.Join(root, path))
			want := os.FileMode(0o644 &^ mask)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("%s: info=%v error=%v want mode=%o", path, info, err, want)
			}
		}
		for _, operation := range []string{"write", "edit", "patch", "move"} {
			path := writeTemp(t, root, "existing", "old\n")
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
			var operationErr error
			switch operation {
			case "write":
				_, operationErr = executor.Write(t.Context(), WriteRequest{Path: "existing", Content: "new\n"})
			case "edit":
				_, operationErr = executor.Edit(t.Context(), EditRequest{Path: "existing", OldString: "old", NewString: "new"})
			case "patch":
				_, operationErr = executor.ApplyPatch(t.Context(), ApplyPatchRequest{Patch: "--- existing\n+++ existing\n@@ -1 +1 @@\n-old\n+new\n"})
			case "move":
				_, operationErr = executor.ApplyPatch(t.Context(), ApplyPatchRequest{Patch: movePatch("existing", "moved/file", "")})
				path = filepath.Join(root, "moved/file")
			}
			if operationErr != nil {
				t.Fatal(operationErr)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("%s: info=%v error=%v want mode=640", operation, info, err)
			}
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mask := range []string{"0077", "0022"} {
		command := exec.CommandContext(t.Context(), executable, "-test.run=^TestWritePermissionsRespectCreationUmask$")
		command.Env = append(os.Environ(), helperMask+"="+mask)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("umask %s: %v\n%s", mask, err, output)
		}
	}
}
