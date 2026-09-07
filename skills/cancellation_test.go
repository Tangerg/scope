package skills_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Tangerg/scope/skills"
)

func TestDiscoveryReturnsCancellationFromCompletedIO(t *testing.T) {
	for _, phase := range []string{"open", "read directory", "read frontmatter"} {
		t.Run(phase, func(t *testing.T) {
			cause := errors.New("discovery stopped")
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			files := fstest.MapFS{}
			wantCloses := 1
			if phase == "read frontmatter" {
				files["safe-skill/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: safe-skill\ndescription: safe\n---")}
				wantCloses = 2
			}
			closed := 0
			repository, err := skills.NewRepository(discoveryFS{
				FS: files, phase: phase, cancel: func() { cancel(cause) }, closed: &closed,
			}, skills.RepositoryConfig{})
			if err != nil {
				t.Fatal(err)
			}
			summaries, err := repository.List(ctx)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Errorf("List error = %v, want cancellation and its cause", err)
			}
			if summaries != nil {
				t.Errorf("canceled discovery returned %d summaries", len(summaries))
			}
			if closed != wantCloses {
				t.Errorf("closed files = %d, want %d", closed, wantCloses)
			}
		})
	}
}

func TestSourceCancellationPreservesStandardAndCustomCauses(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		cause := errors.New("source stopped")
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(cause)
		want := context.Canceled
		if deadline {
			var stop context.CancelFunc
			ctx, stop = context.WithDeadlineCause(t.Context(), time.Now().Add(-time.Second), cause)
			defer stop()
			want = context.DeadlineExceeded
		}
		repository, err := skills.NewRepository(fstest.MapFS{}, skills.RepositoryConfig{})
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range []skills.ResourceSource{repository, skills.Merge(repository)} {
			for _, call := range []func() error{
				func() error { _, err := source.List(ctx); return err },
				func() error { _, err := source.Load(ctx, "safe-skill"); return err },
				func() error { _, err := source.OpenResource(ctx, "safe-skill", "references/note.md"); return err },
				func() error {
					_, _, err := skills.ReadResource(ctx, source, "safe-skill", "references/note.md", 100)
					return err
				},
			} {
				if err := call(); !errors.Is(err, want) || !errors.Is(err, cause) {
					t.Errorf("source error = %v, want %v and its cause", err, want)
				}
			}
		}
	}
}

func TestResourceCancellationClosesFileAndPreservesFailures(t *testing.T) {
	for _, operation := range []string{"repository", "merged", "read"} {
		for _, phase := range []string{"open", "stat"} {
			for _, failed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/failed=%t", operation, phase, failed), func(t *testing.T) {
					cause := errors.New("resource stopped")
					closeErr := errors.New("close failed")
					var ioErr error
					if failed {
						ioErr = errors.New("resource I/O failed")
					}
					ctx, cancel := context.WithCancelCause(t.Context())
					defer cancel(nil)
					closed := 0
					files := cancellationFS{
						FS: fstest.MapFS{
							"safe-skill/SKILL.md":           {Data: []byte("---\nname: safe-skill\ndescription: safe\n---")},
							"safe-skill/references/note.md": {Data: []byte("resource")},
						},
						path: "safe-skill/references/note.md", phase: phase,
						cancel: func() { cancel(cause) }, failure: ioErr, closeErr: closeErr, closed: &closed,
					}
					repository, err := skills.NewRepository(files, skills.RepositoryConfig{})
					if err != nil {
						t.Fatal(err)
					}
					var source skills.ResourceSource = repository
					if operation == "merged" {
						source = skills.Merge(repository)
					}
					if operation == "read" {
						var data []byte
						var truncated bool
						data, truncated, err = skills.ReadResource(ctx, source, "safe-skill", "references/note.md", 100)
						if data != nil || truncated {
							t.Errorf("canceled read = %q, truncated %t", data, truncated)
						}
					} else {
						var file fs.File
						file, err = source.OpenResource(ctx, "safe-skill", "references/note.md")
						if file != nil {
							t.Errorf("canceled open returned a file")
							t.Cleanup(func() {
								if cleanupErr := file.Close(); !errors.Is(cleanupErr, closeErr) {
									t.Errorf("cleanup error = %v, want %v", cleanupErr, closeErr)
								}
							})
						}
					}
					wantCloses := 1
					if phase == "open" && failed {
						wantCloses = 0
					}
					for _, want := range []error{context.Canceled, cause, ioErr, closeErr} {
						if want == nil || (errors.Is(want, closeErr) && wantCloses == 0) {
							continue
						}
						if !errors.Is(err, want) {
							t.Errorf("resource error = %v, want %v", err, want)
						}
					}
					if closed != wantCloses {
						t.Errorf("closed files = %d, want %d", closed, wantCloses)
					}
				})
			}
		}
	}
}

func TestMergedSourcePreservesFailureOnCancellation(t *testing.T) {
	for _, operation := range []string{"list", "load", "open"} {
		t.Run(operation, func(t *testing.T) {
			cause := errors.New("source stopped")
			failure := errors.New("source failed")
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			source := skills.Merge(canceledSource{cancel: func() { cancel(cause) }, failure: failure})
			var err error
			switch operation {
			case "list":
				_, err = source.List(ctx)
			case "load":
				_, err = source.Load(ctx, "safe-skill")
			case "open":
				_, err = source.OpenResource(ctx, "safe-skill", "references/note.md")
			}
			for _, want := range []error{context.Canceled, cause, failure} {
				if !errors.Is(err, want) {
					t.Errorf("source error = %v, want %v", err, want)
				}
			}
		})
	}
}

func TestRepositoryIOCancellationPreservesFailures(t *testing.T) {
	for _, operation := range []string{"list", "load", "read", "reject directory"} {
		for _, phase := range []string{"open", "close"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				cause := errors.New("repository stopped")
				failure := errors.New("repository I/O failed")
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				path := "safe-skill/SKILL.md"
				switch operation {
				case "list":
					path = "."
				case "read", "reject directory":
					path = "safe-skill/references/note.md"
				}
				resource := &fstest.MapFile{Data: []byte("resource")}
				if operation == "reject directory" {
					resource = &fstest.MapFile{Mode: fs.ModeDir}
				}
				closed := 0
				repository, err := skills.NewRepository(cancellationFS{
					FS: fstest.MapFS{
						"safe-skill/SKILL.md":           {Data: []byte("---\nname: safe-skill\ndescription: safe\n---")},
						"safe-skill/references/note.md": resource,
					},
					path: path, phase: phase, cancel: func() { cancel(cause) },
					failure: failure, closeErr: failure, closed: &closed,
				}, skills.RepositoryConfig{})
				if err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "list":
					_, err = repository.List(ctx)
				case "load":
					_, err = repository.Load(ctx, "safe-skill")
				case "read":
					_, _, err = skills.ReadResource(ctx, repository, "safe-skill", "references/note.md", 100)
				case "reject directory":
					_, err = repository.OpenResource(ctx, "safe-skill", "references/note.md")
				}
				for _, want := range []error{context.Canceled, cause, failure} {
					if !errors.Is(err, want) {
						t.Errorf("repository error = %v, want %v", err, want)
					}
				}
				wantCloses := 0
				if phase == "close" {
					wantCloses = 1
				}
				if closed != wantCloses {
					t.Errorf("closed files = %d, want %d", closed, wantCloses)
				}
			})
		}
	}
}

type cancellationFS struct {
	fs.FS
	path     string
	phase    string
	cancel   context.CancelFunc
	failure  error
	closeErr error
	closed   *int
}

func (c cancellationFS) Open(name string) (fs.File, error) {
	if name != c.path {
		return c.FS.Open(name)
	}
	if c.phase == "open" {
		c.cancel()
		if c.failure != nil {
			return nil, c.failure
		}
	}
	file, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return cancellationFile{File: file, source: c}, nil
}

type cancellationFile struct {
	fs.File
	source cancellationFS
}

func (c cancellationFile) Stat() (fs.FileInfo, error) {
	if c.source.phase == "stat" {
		c.source.cancel()
		if c.source.failure != nil {
			return nil, c.source.failure
		}
	}
	return c.File.Stat()
}

func (c cancellationFile) Close() error {
	*c.source.closed++
	if c.source.phase == "close" {
		c.source.cancel()
	}
	return errors.Join(c.File.Close(), c.source.closeErr)
}

func (c cancellationFile) ReadDir(count int) ([]fs.DirEntry, error) {
	return c.File.(fs.ReadDirFile).ReadDir(count)
}

type canceledSource struct {
	skills.ResourceSource
	cancel  context.CancelFunc
	failure error
}

func (c canceledSource) List(context.Context) ([]skills.Summary, error) {
	c.cancel()
	return nil, c.failure
}

func (c canceledSource) Load(context.Context, string) (*skills.Skill, error) {
	c.cancel()
	return nil, c.failure
}

type discoveryFS struct {
	fs.FS
	phase  string
	cancel context.CancelFunc
	closed *int
}

func (d discoveryFS) Open(name string) (fs.File, error) {
	file, err := d.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if d.phase == "open" {
		d.cancel()
	}
	return discoveryFile{File: file, source: d}, nil
}

type discoveryFile struct {
	fs.File
	source discoveryFS
}

func (d discoveryFile) Read(buffer []byte) (int, error) {
	count, err := d.File.Read(buffer)
	if count > 0 && d.source.phase == "read frontmatter" {
		d.source.cancel()
		return count, io.EOF
	}
	return count, err
}

func (d discoveryFile) ReadDir(count int) ([]fs.DirEntry, error) {
	entries, err := d.File.(fs.ReadDirFile).ReadDir(count)
	if d.source.phase == "read directory" {
		d.source.cancel()
	}
	return entries, err
}

func (d discoveryFile) Close() error {
	*d.source.closed++
	return d.File.Close()
}
