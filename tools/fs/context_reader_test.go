package fs

import (
	"context"
	"errors"
	"testing"
)

type cancelingReader struct {
	cancel context.CancelCauseFunc
	cause  error
	err    error
}

func (c cancelingReader) Read(buffer []byte) (int, error) {
	c.cancel(c.cause)
	return copy(buffer, "partial"), c.err
}

func TestContextReaderRetainsReadErrorAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	readErr := errors.New("read failure")
	cause := errors.New("read canceled")
	reader := contextReader{
		ctx: ctx,
		reader: cancelingReader{
			cancel: cancel,
			cause:  cause,
			err:    readErr,
		},
	}
	buffer := make([]byte, 16)
	read, err := reader.Read(buffer)
	if read != len("partial") || string(buffer[:read]) != "partial" {
		t.Fatalf("read = %d, data = %q", read, buffer[:read])
	}
	if !errors.Is(err, readErr) || !errors.Is(err, cause) {
		t.Fatalf("Read() error = %v, want read failure and cancellation", err)
	}
}
