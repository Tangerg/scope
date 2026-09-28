package fs

import (
	"errors"
	"fmt"
)

var (
	ErrNilExecutor        = errors.New("fs: executor must not be nil")
	ErrInvalidRoot        = errors.New("fs: executor root is invalid")
	ErrEmptyPath          = errors.New("fs: path must not be empty")
	ErrInvalidInput       = errors.New("fs: operation input is invalid")
	ErrPathOutsideRoot    = errors.New("fs: path is outside the executor root")
	ErrEmptyPattern       = errors.New("fs: pattern must not be empty")
	ErrRipgrepUnavailable = errors.New("fs: ripgrep is unavailable")

	// ErrMutationRejected proves that a write, edit, or patch stopped before
	// changing any file or directory. Backends must not return it after commit
	// begins, even when the only acknowledged changes are partial.
	ErrMutationRejected = errors.New("fs: mutation rejected without changes")
	ErrBinaryFile       = errors.New("fs: file appears to be binary; only text files are supported")

	// ErrFileTooLarge reports that the operation returned no partial file.
	ErrFileTooLarge = errors.New("fs: file exceeds the operation input limit")

	// ErrLineTooLarge preserves the one-based offending line through
	// ReadLineNumber.
	ErrLineTooLarge = errors.New("fs: line exceeds the operation line limit")
)

type lineLimitError struct {
	path  string
	line  int
	limit int
}

func (l *lineLimitError) Error() string {
	return fmt.Sprintf("%s: %s line %d exceeds %d bytes", ErrLineTooLarge, l.path, l.line, l.limit)
}

func (l *lineLimitError) Unwrap() error { return ErrLineTooLarge }

// ReadLineNumber returns the one-based line attached to an
// [ErrLineTooLarge] failure, or zero when the error is not line-specific.
func ReadLineNumber(err error) int {
	if target, ok := errors.AsType[*lineLimitError](err); ok {
		return target.line
	}
	return 0
}
