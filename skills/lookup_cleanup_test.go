package skills_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Tangerg/scope/skills"
)

type cancelOnCloseFS struct {
	files    fs.FS
	cancel   context.CancelCauseFunc
	cause    error
	closeErr error
}

func (c cancelOnCloseFS) Open(name string) (fs.File, error) {
	file, err := c.files.Open(name)
	if err != nil {
		return nil, err
	}
	return &cancelOnCloseFile{file: file, cancel: c.cancel, cause: c.cause, closeErr: c.closeErr}, nil
}

type cancelOnCloseFile struct {
	file     fs.File
	cancel   context.CancelCauseFunc
	cause    error
	closeErr error
}

func (c *cancelOnCloseFile) Read(data []byte) (int, error) { return c.file.Read(data) }

func (c *cancelOnCloseFile) Stat() (fs.FileInfo, error) { return c.file.Stat() }

func (c *cancelOnCloseFile) Close() error {
	c.cancel(c.cause)
	return errors.Join(c.file.Close(), c.closeErr)
}

func TestLookupPreservesCancellationAndConcurrentCleanupFailure(t *testing.T) {
	cause := errors.New("host canceled discovery")
	cleanup := errors.New("close failed")
	for _, test := range []struct {
		name     string
		content  string
		closeErr error
	}{
		{name: "valid metadata with close failure", content: "---\nname: guide\ndescription: Guide.\n---\nbody", closeErr: cleanup},
		{name: "malformed metadata", content: "not frontmatter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			repository, err := skills.NewRepository(cancelOnCloseFS{
				files: fstest.MapFS{"guide/SKILL.md": {Data: []byte(test.content)}}, cancel: cancel, cause: cause, closeErr: test.closeErr,
			}, skills.RepositoryConfig{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = repository.Lookup(ctx, "guide")
			if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || (test.closeErr != nil && !errors.Is(err, test.closeErr)) {
				t.Fatalf("Lookup lost concurrent causes: %v", err)
			}
		})
	}
}
