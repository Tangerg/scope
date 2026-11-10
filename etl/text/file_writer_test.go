package text_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/text"
)

func TestFileWriterDefaultsToTextAndSupportsAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.txt")
	first, err := text.NewFileWriter(text.FileWriterConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := document.NewDocument("first", nil)
	if writeErr := first.Write(t.Context(), []*document.Document{doc}); writeErr != nil {
		t.Fatal(writeErr)
	}

	second, err := text.NewFileWriter(text.FileWriterConfig{
		Path: path, Append: true, DocumentMarkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = document.NewDocument("second", nil)
	if writeErr := second.Write(t.Context(), []*document.Document{doc}); writeErr != nil {
		t.Fatal(writeErr)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := string(data)
	if contents != "first\n\n### Index: 0\nsecond\n\n" {
		t.Fatalf("file contents = %q", contents)
	}
}

func TestFileWriterRejectsMediaItCannotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.txt")
	writer, err := text.NewFileWriter(text.FileWriterConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := media.NewURI("image/png", "https://example.com/figure.png")
	if err != nil {
		t.Fatal(err)
	}
	figure, err := document.NewDocument("caption", payload)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := writer.Write(t.Context(), []*document.Document{figure}); !errors.Is(writeErr, document.ErrUnsupportedMedia) {
		t.Fatalf("Write error = %v, want ErrUnsupportedMedia", writeErr)
	}
}

func ExampleFileWriter() {
	directory, err := os.MkdirTemp("", "scope-text-example-")
	if err != nil {
		panic(err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			panic(cleanupErr)
		}
	}()
	path := filepath.Join(directory, "documents.txt")
	writer, err := text.NewFileWriter(text.FileWriterConfig{Path: path})
	if err != nil {
		panic(err)
	}
	doc, err := document.NewDocument("Retrieved evidence.", nil)
	if err != nil {
		panic(err)
	}
	if writeErr := writer.Write(context.Background(), []*document.Document{doc}); writeErr != nil {
		panic(writeErr)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	fmt.Print(string(contents))
	// Output: Retrieved evidence.
}

func TestFileWriterRequiresPath(t *testing.T) {
	if _, err := text.NewFileWriter(text.FileWriterConfig{}); err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestFileWriterRejectsTypedNilFormatter(t *testing.T) {
	var formatter *etl.MetadataHeaderFormatter
	if _, err := text.NewFileWriter(text.FileWriterConfig{
		Path:      filepath.Join(t.TempDir(), "documents.txt"),
		Formatter: formatter,
	}); err == nil {
		t.Fatal("expected typed nil formatter error")
	}
}

func TestFileWriterHonorsCanceledContextBeforeOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.txt")
	writer, err := text.NewFileWriter(text.FileWriterConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := writer.Write(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file stat error = %v, want os.ErrNotExist", err)
	}
}

func TestFileWriterPreservesExistingFileOnRenderFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.txt")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("format failed")
	writer, err := text.NewFileWriter(text.FileWriterConfig{
		Path: path,
		Formatter: document.FormatterFunc(func(*document.Document) (string, error) {
			return "", want
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := document.NewDocument("replacement", nil)
	if writeErr := writer.Write(t.Context(), []*document.Document{doc}); !errors.Is(writeErr, want) {
		t.Fatalf("Write error = %v", writeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("existing file changed to %q", data)
	}
}

func TestFileWriterRejectsInvalidDocumentsBeforeOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.txt")
	writer, err := text.NewFileWriter(text.FileWriterConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []*document.Document{nil, {Text: "bad\xff"}} {
		if err := writer.Write(t.Context(), []*document.Document{doc}); err == nil {
			t.Fatal("invalid document accepted")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid input created destination: %v", err)
		}
	}
}

func TestFileWriterRejectsDirectoryDestination(t *testing.T) {
	for _, appendMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("append=%t", appendMode), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "documents.txt")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			writer, err := text.NewFileWriter(text.FileWriterConfig{Path: path, Append: appendMode})
			if err != nil {
				t.Fatal(err)
			}
			if writeErr := writer.Write(t.Context(), []*document.Document{{Text: "content"}}); writeErr == nil {
				t.Fatal("directory destination accepted")
			}
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				t.Fatalf("destination directory was changed: %v, %v", info, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "documents.txt" {
				t.Fatalf("failed write left temporary files: %v, %v", entries, err)
			}
		})
	}
}

func TestFileWriterReportsMissingDestinationDirectory(t *testing.T) {
	dir := t.TempDir()
	writer, err := text.NewFileWriter(text.FileWriterConfig{Path: filepath.Join(dir, "missing", "documents.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := writer.Write(t.Context(), []*document.Document{{Text: "content"}}); !errors.Is(writeErr, os.ErrNotExist) {
		t.Fatalf("Write error = %v, want os.ErrNotExist", writeErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed write created destination entries: %v, %v", entries, err)
	}
}

// Replacement commits through a sibling file renamed over the destination, so
// it must keep the destination's permissions and leave no sibling behind.
func TestFileWriterReplacesAtomicallyAndKeepsPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "documents.txt")
	if err := os.WriteFile(path, []byte("previous\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := text.NewFileWriter(text.FileWriterConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := document.NewDocument("replacement", nil)
	if err = writer.Write(t.Context(), []*document.Document{doc}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement\n\n" {
		t.Fatalf("contents = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the destination: %v", len(entries), err)
	}
}
