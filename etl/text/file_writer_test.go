package text_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tangerg/scope/core/document"
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
	var formatter *etl.SimpleFormatter
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
		Formatter: etl.FormatterFunc(func(*document.Document) (string, error) {
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
