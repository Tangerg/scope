package fs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// Default result caps applied when the caller leaves MaxResults at 0.
// The defaults prevent LLM context bloat without forcing the LLM to pass a
// cap on every call.
const (
	defaultGrepMaxResults = 250
	defaultGlobMaxResults = 100
	maximumSearchResults  = 1000
	maximumContextLines   = 20
)

// GlobWalk visits only matches, so cancellation belongs on its Stat and ReadDir
// boundaries as well: a directory tree with no matches still performs I/O.
type globFilesystem struct {
	fs.FS
	ctx context.Context
}

func (g globFilesystem) Stat(name string) (fs.FileInfo, error) {
	if err := g.ctx.Err(); err != nil {
		return nil, err
	}
	info, err := fs.Stat(g.FS, name)
	return info, errors.Join(err, g.ctx.Err())
}

func (g globFilesystem) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := g.ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(g.FS, name)
	return entries, errors.Join(err, g.ctx.Err())
}

func validateGlobPattern(pattern string) error {
	if filepath.IsAbs(pattern) {
		return fmt.Errorf("%w: glob pattern %q", ErrPathOutsideRoot, pattern)
	}
	for _, component := range strings.Split(filepath.ToSlash(pattern), "/") {
		if component == ".." {
			return fmt.Errorf("%w: glob pattern %q", ErrPathOutsideRoot, pattern)
		}
	}
	return nil
}
