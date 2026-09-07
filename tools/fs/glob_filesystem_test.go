package fs

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/bmatcuk/doublestar/v4"
)

func TestGlobWalkStopsAtCanceledFilesystemOperation(t *testing.T) {
	for _, testCase := range []struct {
		name, pattern string
		cancelReadDir bool
	}{
		{name: "directory without matches", pattern: "**/*.missing", cancelReadDir: true},
		{name: "literal file", pattern: "nested/deeper/file.txt"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			backend := &cancelingGlobFilesystem{
				MapFS:         fstest.MapFS{"nested/deeper/file.txt": &fstest.MapFile{Data: []byte("text")}},
				ctx:           ctx,
				cancel:        cancel,
				cancelReadDir: testCase.cancelReadDir,
			}
			var matches int
			err := doublestar.GlobWalk(globFilesystem{FS: backend, ctx: ctx}, testCase.pattern,
				func(string, fs.DirEntry) error {
					matches++
					return nil
				}, doublestar.WithFailOnIOErrors())
			if !errors.Is(err, context.Canceled) || backend.operationsAfterCancel != 0 || matches != 0 {
				t.Fatalf("walk = %d operations after cancellation, %d matches, error %v", backend.operationsAfterCancel, matches, err)
			}
		})
	}
}

type cancelingGlobFilesystem struct {
	fstest.MapFS
	ctx                   context.Context
	cancel                context.CancelFunc
	cancelReadDir         bool
	operationsAfterCancel int
}

func (c *cancelingGlobFilesystem) ReadDir(name string) ([]fs.DirEntry, error) {
	if c.ctx.Err() != nil {
		c.operationsAfterCancel++
	}
	entries, err := c.MapFS.ReadDir(name)
	if c.cancelReadDir {
		c.cancel()
	}
	return entries, err
}

func (c *cancelingGlobFilesystem) Stat(name string) (fs.FileInfo, error) {
	if c.ctx.Err() != nil {
		c.operationsAfterCancel++
	}
	info, err := c.MapFS.Stat(name)
	if !c.cancelReadDir {
		c.cancel()
	}
	return info, err
}
