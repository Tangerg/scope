package httpclient

import (
	"errors"
	"io"
	"net/http"
)

// The SDK still parses SSE. Counting wire bytes up to its empty-line boundary
// prevents a multi-line event from bypassing the SDK's per-line scanner limit.
type eventStreamBody struct {
	source     io.ReadCloser
	limit      int64
	eventBytes int64
	lineBytes  int64
	lastByte   byte
	err        error
}

func (e *eventStreamBody) Read(buffer []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	readSize := int64(len(buffer))
	if remaining := e.limit - e.eventBytes; remaining < readSize {
		readSize = remaining + 1
	}
	count, err := e.source.Read(buffer[:readSize])
	for index, value := range buffer[:count] {
		if e.eventBytes == e.limit {
			e.err = errors.Join(&http.MaxBytesError{Limit: e.limit}, err)
			return index, e.err
		}
		e.eventBytes++
		if value == '\n' {
			if e.lineBytes == 0 || e.lineBytes == 1 && e.lastByte == '\r' {
				e.eventBytes = 0
			}
			e.lineBytes = 0
		} else {
			e.lineBytes++
		}
		e.lastByte = value
	}
	e.err = err
	return count, err
}

func (e *eventStreamBody) Close() error { return e.source.Close() }
