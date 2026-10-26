package contextreader

import (
	"context"
	"errors"
	"io"
)

// A source may return bytes and an error while cancellation occurs. Neither
// observation may replace the other; the source still owns interrupting I/O.
type Reader struct {
	ctx    context.Context
	source io.Reader
}

func New(ctx context.Context, source io.Reader) Reader {
	return Reader{ctx: ctx, source: source}
}

func (r Reader) Read(buffer []byte) (int, error) {
	if cause := context.Cause(r.ctx); cause != nil {
		return 0, cause
	}
	read, err := r.source.Read(buffer)
	if cause := context.Cause(r.ctx); cause != nil {
		return read, errors.Join(err, cause)
	}
	return read, err
}
