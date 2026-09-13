//go:build unix

package fs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGrepReadsOpenedFileAfterPathReplacement(t *testing.T) {
	executable, err := exec.LookPath("rg")
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	selected := filepath.Join(root, "selected.txt")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if checkErr := os.WriteFile(selected, []byte("inside-marker\n"), 0o600); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := os.WriteFile(outside, []byte("outside-marker\n"), 0o600); checkErr != nil {
		t.Fatal(checkErr)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "validated")
	script := fmt.Sprintf("#!/bin/sh\nif [ -f %q ]; then\n /bin/mv %q %q\n /bin/ln -s %q %q\nfi\n/usr/bin/touch %q\nexec %q \"$@\"\n", marker, selected, selected+".old", outside, selected, marker, executable)
	if checkErr := os.WriteFile(filepath.Join(bin, "rg"), []byte(script), 0o700); checkErr != nil {
		t.Fatal(checkErr)
	}
	t.Setenv("PATH", bin)
	result, err := mustLocalExecutor(t, root).Grep(t.Context(), GrepInput{Path: "selected.txt", Pattern: "marker"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 1 || result.Lines[0].Text != "inside-marker" || result.Lines[0].Path != "selected.txt" {
		t.Fatalf("search escaped opened file: %+v", result)
	}
}
