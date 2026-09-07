package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Both checks matter: reject known devices before opening, and reject a file
// replaced between Stat and Open. Nonblocking open prevents a raced FIFO from
// holding the caller before the descriptor can be inspected.
func openRegularRootFile(ctx context.Context, root *os.Root, path string) (*os.File, os.FileInfo, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	info, err := root.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("fs: %s: unsupported file mode %s", path, info.Mode().Type())
	}
	file, err := root.OpenFile(path, regularReadFlags, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err = file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("fs: %s: unsupported file mode %s", path, info.Mode().Type())
	}
	err = errors.Join(err, context.Cause(ctx))
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	return file, info, nil
}
