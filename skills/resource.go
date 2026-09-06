package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/samber/lo"
)

// ReadResource reads at most maxBytes from a bundled skill resource. The
// truncated result is valid content but must not be treated as the complete
// resource. maxBytes must be positive.
func ReadResource(
	ctx context.Context,
	src ResourceSource,
	name string,
	resource string,
	maxBytes int64,
) ([]byte, bool, error) {
	if lo.IsNil(src) {
		return nil, false, ErrNilSource
	}
	if err := ValidateName(name); err != nil {
		return nil, false, err
	}
	if err := validateResourcePath(resource); err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 || maxBytes > maxBoundedReadBytes {
		return nil, false, fmt.Errorf("%w: max bytes must be positive and safely bounded", ErrInvalidLimit)
	}
	operation := fmt.Sprintf("read resource %q/%q", name, resource)
	if err := contextError(ctx, operation); err != nil {
		return nil, false, err
	}
	file, err := src.OpenResource(ctx, name, resource)
	file, err = checkedResourceFile(ctx, operation, name, resource, file, err)
	if err != nil {
		return nil, false, err
	}
	data, truncated, err := readBounded(ctx, file, maxBytes)
	closeErr := file.Close()
	err = errors.Join(
		resourceIOError("read", name, resource, err),
		resourceIOError("close", name, resource, closeErr),
	)
	if err != nil {
		return nil, false, err
	}
	return data, truncated, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (c contextReader) Read(buffer []byte) (int, error) {
	if err := contextError(c.ctx, "read"); err != nil {
		return 0, err
	}
	read, err := c.reader.Read(buffer)
	if contextErr := contextError(c.ctx, "read"); contextErr != nil {
		return read, errors.Join(err, contextErr)
	}
	return read, err
}

func readBounded(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	limited := io.LimitReader(contextReader{ctx: ctx, reader: reader}, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return data[:maxBytes], true, nil
	}
	return data, false, nil
}

func checkedResourceFile(
	ctx context.Context,
	operation string,
	name string,
	resource string,
	file fs.File,
	err error,
) (fs.File, error) {
	if ctxErr := contextError(ctx, operation); ctxErr != nil {
		return nil, errors.Join(ctxErr, closeResourceFile(name, resource, file))
	}
	if err != nil {
		return nil, errors.Join(err, closeResourceFile(name, resource, file))
	}
	if lo.IsNil(file) {
		return nil, fmt.Errorf("skills: %s: %w", operation, ErrNilResourceFile)
	}
	info, statErr := file.Stat()
	if statErr != nil {
		return nil, errors.Join(
			resourceIOError("stat", name, resource, statErr),
			closeResourceFile(name, resource, file),
		)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(
			fmt.Errorf("skills: %s: %w: mode %s", operation, ErrResourceNotRegular, info.Mode().Type()),
			closeResourceFile(name, resource, file),
		)
	}
	return file, nil
}

func closeResourceFile(name, resource string, file fs.File) error {
	if lo.IsNil(file) {
		return nil
	}
	return resourceIOError("close", name, resource, file.Close())
}

func resourceIOError(operation, name, resource string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("skills: %s resource %q/%q: %w", operation, name, resource, err)
}

func validateResourcePath(resource string) error {
	if resource == "." || !fs.ValidPath(resource) || strings.ContainsRune(resource, '\\') {
		return fmt.Errorf("%w: %q", ErrResourcePath, resource)
	}
	return nil
}
