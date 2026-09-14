package fs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeText_StripsBOM(t *testing.T) {
	in := []byte("\xEF\xBB\xBFhello\n")
	text, hadBOM, hadCRLF := normalizeText(in)
	if !hadBOM {
		t.Error("hadBOM = false, want true")
	}
	if hadCRLF {
		t.Error("hadCRLF = true, want false")
	}
	if text != "hello\n" {
		t.Errorf("text = %q, want %q", text, "hello\n")
	}
}

func TestNormalizeText_ConvertsCRLF(t *testing.T) {
	in := []byte("a\r\nb\r\nc\n")
	text, hadBOM, hadCRLF := normalizeText(in)
	if hadBOM {
		t.Error("hadBOM = true, want false")
	}
	if !hadCRLF {
		t.Error("hadCRLF = false, want true")
	}
	if text != "a\nb\nc\n" {
		t.Errorf("text = %q, want %q", text, "a\nb\nc\n")
	}
}

func TestNormalizeText_BOMAndCRLF(t *testing.T) {
	in := []byte("\xEF\xBB\xBFa\r\nb\r\n")
	text, hadBOM, hadCRLF := normalizeText(in)
	if !hadBOM || !hadCRLF {
		t.Errorf("hadBOM=%v hadCRLF=%v, want both true", hadBOM, hadCRLF)
	}
	if text != "a\nb\n" {
		t.Errorf("text = %q, want %q", text, "a\nb\n")
	}
}

func TestRestoreFormat_Roundtrip(t *testing.T) {
	cases := []struct {
		name    string
		input   []byte
		want    []byte
		hadBOM  bool
		hadCRLF bool
	}{
		{"plain", []byte("a\nb\n"), []byte("a\nb\n"), false, false},
		{"crlf only", []byte("a\r\nb\r\n"), []byte("a\r\nb\r\n"), false, true},
		{"bom only", []byte("\xEF\xBB\xBFa\nb\n"), []byte("\xEF\xBB\xBFa\nb\n"), true, false},
		{"bom + crlf", []byte("\xEF\xBB\xBFa\r\nb\r\n"), []byte("\xEF\xBB\xBFa\r\nb\r\n"), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, hadBOM, hadCRLF := normalizeText(tc.input)
			if hadBOM != tc.hadBOM || hadCRLF != tc.hadCRLF {
				t.Errorf("flags: hadBOM=%v hadCRLF=%v; want hadBOM=%v hadCRLF=%v",
					hadBOM, hadCRLF, tc.hadBOM, tc.hadCRLF)
			}
			got := restoreFormat(text, hadBOM, hadCRLF)
			if string(got) != string(tc.want) {
				t.Errorf("restore = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAtomicWriteFile_WritesAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const relativePath = "file.txt"
	path := filepath.Join(dir, relativePath)
	if writeErr := atomicWriteRootFile(root, relativePath, []byte("hello"), new(os.FileMode(0o600))); writeErr != nil {
		t.Fatalf("atomicWriteRootFile: %v", writeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want %o", got, 0o600)
	}
}

func TestAtomicWriteFile_NoLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if writeErr := atomicWriteRootFile(root, "file.txt", []byte("x"), nil); writeErr != nil {
		t.Fatalf("atomicWriteRootFile: %v", writeErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir has %d entries, want 1: %v", len(entries), names)
	}
}

func TestLooksBinary(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", nil, false},
		{"plain text", []byte("hello world\n"), false},
		{"utf-8 text", []byte("héllo 世界\n"), false},
		{"nul byte", []byte("abc\x00def"), true},
		{"nul past sniff window", append(bytes.Repeat([]byte{'a'}, binarySniffLen+10), 0), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksBinary(tc.data); got != tc.want {
				t.Errorf("looksBinary = %v, want %v", got, tc.want)
			}
		})
	}
}
