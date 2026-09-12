package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	defaultReadInputBytes     int64 = 8 << 20
	defaultReadLineBytes            = 1 << 20
	defaultReadOutputBytes          = 1 << 20
	defaultMutationInputBytes int64 = 8 << 20
	formatDetectionBytes      int64 = 64 << 10
)

type readLimits struct {
	inputBytes  int64
	lineBytes   int
	outputBytes int
}

func positiveOr[T ~int | ~int64](value, fallback T) T {
	if value > 0 {
		return value
	}
	return fallback
}

func readBoundedRootFile(ctx context.Context, root *os.Root, path string, maxBytes int64) (_ []byte, err error) {
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	file, info, err := openRegularRootFile(ctx, root, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s uses %d bytes; limit is %d", ErrFileTooLarge, path, info.Size(), maxBytes)
	}
	source := io.LimitReader(contextReader{ctx: ctx, reader: file}, maxBytes+1)
	data, err := io.ReadAll(source)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %s grew beyond %d bytes", ErrFileTooLarge, path, maxBytes)
	}
	return data, nil
}

func detectRootFormat(ctx context.Context, root *os.Root, path string) (hadBOM, hadCRLF bool, err error) {
	file, _, err := openRegularRootFile(ctx, root, path)
	if err != nil {
		return false, false, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	prefix, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: file}, formatDetectionBytes))
	if err != nil {
		return false, false, err
	}
	return bytes.HasPrefix(prefix, []byte(utf8BOM)), bytes.Contains(prefix, []byte("\r\n")), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (c contextReader) Read(buffer []byte) (int, error) {
	if cause := context.Cause(c.ctx); cause != nil {
		return 0, cause
	}
	read, err := c.reader.Read(buffer)
	if cause := context.Cause(c.ctx); cause != nil {
		return read, cause
	}
	return read, err
}

// binarySniffLen matches git's heuristic — a NUL in the first 8 KiB
// means the file is treated as binary.
const binarySniffLen = 8192

func looksBinary(data []byte) bool {
	sniff := data
	if len(sniff) > binarySniffLen {
		sniff = sniff[:binarySniffLen]
	}
	return bytes.IndexByte(sniff, 0) >= 0
}
