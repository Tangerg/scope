package text

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

// New text files carry no executable bits; the process umask narrows the
// remaining permissions for the caller's environment.
const createdTextFileMode os.FileMode = 0o666

// FileWriterConfig fixes the output authority and filename policy at
// construction time.
type FileWriterConfig struct {
	// Path is required. Existing files are replaced unless Append is true.
	Path string
	// DocumentMarkers adds an index header before each document.
	DocumentMarkers bool
	// Append preserves existing file contents.
	Append bool
	// Formatter renders each document. Nil writes document text only.
	Formatter etl.Formatter
}

// FileWriter persists documents as plain text. It honors Append,
// optionally injects document-marker headers, and calls [*os.File].Sync
// before returning so callers can rely on durability when the call
// completes.
//
// Example:
//
//	w, err := text.NewFileWriter(text.FileWriterConfig{
//	    Path:            "out.txt",
//	    DocumentMarkers: true,
//	})
//	err = w.Write(ctx, docs)
type FileWriter struct {
	path            string
	documentMarkers bool
	append          bool
	formatter       etl.Formatter
}

// NewFileWriter validates its filesystem boundary before accepting
// documents.
func NewFileWriter(config FileWriterConfig) (*FileWriter, error) {
	if config.Path == "" {
		return nil, errors.New("etl: output path is required")
	}
	if config.Formatter == nil {
		config.Formatter = etl.TextFormatter{}
	} else if lo.IsNil(config.Formatter) {
		return nil, errors.New("etl: formatter must not be a typed nil")
	}
	return &FileWriter{
		path:            config.Path,
		documentMarkers: config.DocumentMarkers,
		append:          config.Append,
		formatter:       config.Formatter,
	}, nil
}

// Write validates and renders every document before opening the destination,
// so document or formatting failures leave an existing file untouched. Once
// the destination is opened, the prepared payload is committed even if ctx is
// subsequently canceled. Close errors are joined with earlier I/O failures.
func (f *FileWriter) Write(ctx context.Context, docs []*document.Document) (err error) {
	payload, err := f.render(ctx, docs)
	if err != nil {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	file, err := os.OpenFile(f.path, f.openFlags(), createdTextFileMode)
	if err != nil {
		return fmt.Errorf("etl: open output %q: %w", f.path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("etl: close output %q: %w", f.path, closeErr))
		}
	}()

	if writeErr := f.write(payload, file); writeErr != nil {
		return fmt.Errorf("etl: write output %q: %w", f.path, writeErr)
	}
	return nil
}

func (f *FileWriter) openFlags() int {
	if f.append {
		return os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	return os.O_CREATE | os.O_WRONLY | os.O_TRUNC
}

func (f *FileWriter) render(ctx context.Context, docs []*document.Document) (string, error) {
	var payload strings.Builder
	for i, doc := range docs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if doc == nil {
			return "", fmt.Errorf("etl: render output %q: document %d: %w", f.path, i, etl.ErrNilDocument)
		}
		if err := doc.Validate(); err != nil {
			return "", fmt.Errorf("etl: render output %q: validate document %d: %w", f.path, i, err)
		}
		rendered, err := f.renderDocument(i, doc)
		if err != nil {
			return "", fmt.Errorf("etl: render output %q: document %d: %w", f.path, i, err)
		}
		payload.WriteString(rendered)
	}
	return payload.String(), nil
}

func (*FileWriter) write(payload string, file *os.File) error {
	buffered := bufio.NewWriter(file)
	if _, err := io.WriteString(buffered, payload); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("flush buffered output: %w", err)
	}
	return file.Sync()
}

func (f *FileWriter) renderDocument(index int, doc *document.Document) (string, error) {
	var buf strings.Builder

	if f.documentMarkers {
		buf.WriteString("### Index: ")
		buf.WriteString(strconv.Itoa(index))
		buf.WriteString("\n")
	}

	rendered, err := f.formatter.Format(doc)
	if err != nil {
		return "", err
	}
	buf.WriteString(rendered)
	buf.WriteString("\n\n")
	return buf.String(), nil
}
