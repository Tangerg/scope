package skills_test

import (
	"context"
	"errors"
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
