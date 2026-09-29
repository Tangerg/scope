package fs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

const (
	defaultGrepMaxResults = 250
	defaultGlobMaxResults = 100
	maximumSearchResults  = 1000
	maximumContextLines   = 20
)

func searchResultLimit(requested, fallback int) (int, error) {
	if requested < 0 || requested > maximumSearchResults {
		return 0, fmt.Errorf("%w: max_results must be between 0 and %d", ErrInvalidInput, maximumSearchResults)
	}
	if requested == 0 {
		return fallback, nil
	}
	return requested, nil
}

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
	for component := range strings.SplitSeq(filepath.ToSlash(pattern), "/") {
		if component == ".." {
			return fmt.Errorf("%w: glob pattern %q", ErrPathOutsideRoot, pattern)
		}
	}
	return nil
}

// Walk order is not sorted, so a bounded result keeps the lexically smallest
// paths seen so far; the answer is deterministic however the walk proceeds.
type sortedPathSet struct {
	limit     int
	paths     []string
	truncated bool
}

func (s *sortedPathSet) add(path string) {
	index, exists := slices.BinarySearch(s.paths, path)
	if exists {
		return
	}
	if len(s.paths) >= s.limit {
		s.truncated = true
		if index >= s.limit {
			return
		}
		s.paths = s.paths[:s.limit-1]
	}
	s.paths = slices.Insert(s.paths, index, path)
}
