package text

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

// New text files carry no executable bits; the process umask narrows the
// remaining permissions for the caller's environment.
const createdTextFileMode os.FileMode = 0o666

// temporaryNameEntropyBytes makes a sibling replacement file name collide with
// neither another writer's nor an existing file.
const temporaryNameEntropyBytes = 16

type FileWriterConfig struct {
	// Path is required. Existing files are replaced unless Append is true.
	Path string
	// DocumentMarkers adds a header naming each document's index within its
	// Write call, so appended batches each number from zero.
	DocumentMarkers bool
	Append          bool
	// Formatter renders each document. Nil uses [document.TextFormatter],
	// which rejects media rather than dropping it from the file.
	Formatter document.Formatter
}

// FileWriter calls os.File.Sync before a successful Write returns.
type FileWriter struct {
	path            string
	documentMarkers bool
	append          bool
	formatter       document.Formatter
}

func NewFileWriter(config FileWriterConfig) (*FileWriter, error) {
	if config.Path == "" {
		return nil, errors.New("etl: output path is required")
	}
	if config.Formatter == nil {
		config.Formatter = document.TextFormatter{}
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

// Write validates and renders every document before touching the
// destination, so document or formatting failures leave an existing file
// untouched. A replacing Write also leaves it intact when writing fails: the
// payload is committed to a sibling file and renamed over the destination.
// Once writing starts, the payload is committed even if ctx is subsequently
// canceled. Close errors are joined with earlier I/O failures.
func (f *FileWriter) Write(ctx context.Context, docs []*document.Document) error {
	payload, err := f.render(ctx, docs)
	if err != nil {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if f.append {
		err = f.appendPayload(payload)
	} else {
		err = f.replace(payload)
	}
	if err != nil {
		return fmt.Errorf("etl: write output %q: %w", f.path, err)
	}
	return nil
}

func (f *FileWriter) appendPayload(payload string) (err error) {
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, createdTextFileMode)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return f.write(payload, file)
}

// replace keeps POSIX rename atomic by writing a sibling file in the
// destination's directory. An existing destination keeps its permissions; a
// new one is created with createdTextFileMode subject to umask.
func (f *FileWriter) replace(payload string) (err error) {
	var preserved *os.FileMode
	if info, statErr := os.Stat(f.path); statErr == nil {
		preserved = new(info.Mode().Perm())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	var entropy [temporaryNameEntropyBytes]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(f.path), "."+filepath.Base(f.path)+"."+hex.EncodeToString(entropy[:]))
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, createdTextFileMode)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if removeErr := os.Remove(temporary); !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	if err = f.write(payload, file); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return err
	}
	if preserved != nil {
		if err = os.Chmod(temporary, *preserved); err != nil {
			return err
		}
	}
	return os.Rename(temporary, f.path)
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

// The payload is fully rendered in memory and written once, so buffering adds
// nothing; a single WriteString followed by Sync commits it durably.
func (*FileWriter) write(payload string, file *os.File) error {
	if _, err := file.WriteString(payload); err != nil {
		return err
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
