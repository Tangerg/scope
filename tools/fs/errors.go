package fs

import (
	"errors"
	"fmt"
)

var (
	// ErrNilExecutor rejects a tool without the backend capability it consumes.
	ErrNilExecutor = errors.New("fs: executor must not be nil")
	// ErrInvalidRoot identifies an authority root that cannot be fixed safely.
	ErrInvalidRoot = errors.New("fs: executor root is invalid")
	// ErrEmptyPath rejects an operation with no target identity.
	ErrEmptyPath = errors.New("fs: path must not be empty")
	// ErrInvalidInput identifies operation arguments rejected by the backend.
	ErrInvalidInput = errors.New("fs: operation input is invalid")

	// ErrPathOutsideRoot identifies an attempted authority escape.
	ErrPathOutsideRoot = errors.New("fs: path is outside the executor root")

	// ErrEmptyPattern rejects searches that do not define a query.
	ErrEmptyPattern = errors.New("fs: pattern must not be empty")

	// ErrRipgrepUnavailable reports that the local grep backend lacks its engine.
	ErrRipgrepUnavailable = errors.New("fs: ripgrep is unavailable")

	// ErrEditRejected proves that Edit rejected the request before any mutation.
	ErrEditRejected = errors.New("fs: edit rejected without mutation")

	// ErrBinaryFile prevents text tools from silently corrupting binary content.
	ErrBinaryFile = errors.New("fs: file appears to be binary; only text files are supported")

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
