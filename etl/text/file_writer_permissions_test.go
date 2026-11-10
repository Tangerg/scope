package text

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplacementPermissionsAreRestrictedBeforeWriting(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o640, 0o400, 0o000} {
		writer := &FileWriter{path: filepath.Join(t.TempDir(), "private.txt")}
		file, err := writer.openReplacement(mode)
		if err != nil {
			t.Fatal(err)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil {
			t.Fatalf("stat=%v close=%v", statErr, closeErr)
		}
		if actual := info.Mode().Perm(); actual & ^mode != 0 {
			t.Fatalf("temporary mode=%#o grants permissions beyond %#o before writing", actual, mode)
		}
		if info.Size() != 0 {
			t.Fatal("replacement already contains data")
		}
	}
}
