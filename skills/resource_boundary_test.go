package skills_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Tangerg/scope/skills"
)

type resourceCountingFS struct {
	fs.FS
	opens map[string]int
	stats map[string]int
}

func (r *resourceCountingFS) Open(name string) (fs.File, error) {
	r.opens[name]++
	file, err := r.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return resourceCountingFile{File: file, source: r, name: name}, nil
}

type resourceCountingFile struct {
	fs.File
	source *resourceCountingFS
	name   string
}

func (r resourceCountingFile) Stat() (fs.FileInfo, error) {
	r.source.stats[r.name]++
	return r.File.Stat()
}

func TestMergedResourceReadsOwningSkillAndChecksFileOnce(t *testing.T) {
	backing := &resourceCountingFS{
		FS: fstest.MapFS{
			"demo/SKILL.md": {Data: []byte("---\nname: demo\ndescription: Demonstration\n---\nInstructions")},
			"demo/note":     {Data: []byte("resource")},
		},
		opens: map[string]int{}, stats: map[string]int{},
	}
	repository, err := skills.NewRepository(backing, skills.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	data, truncated, err := skills.ReadResource(t.Context(), skills.Merge(skills.Merge(repository)), "demo", "note", 1024)
	if err != nil || truncated || string(data) != "resource" {
		t.Fatalf("ReadResource = %q, %t, %v", data, truncated, err)
	}
	if backing.opens["demo/SKILL.md"] != 1 || backing.opens["demo/note"] != 1 || backing.stats["demo/note"] != 1 {
		t.Fatalf("one resource operation repeated filesystem work: opens %v, stats %v", backing.opens, backing.stats)
	}
}

func TestResourceAbsenceRetainsSkillOwnership(t *testing.T) {
	repository, err := skills.NewRepository(fstest.MapFS{
		"demo/SKILL.md": {Data: []byte("---\nname: demo\ndescription: Demonstration\n---\nInstructions")},
	}, skills.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.OpenResource(t.Context(), "missing", "note")
	if !errors.Is(err, skills.ErrSkillNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing skill = %v", err)
	}
	_, err = repository.OpenResource(t.Context(), "demo", "note")
	if errors.Is(err, skills.ErrSkillNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing resource = %v", err)
	}
}

func TestMergeDoesNotHideCancellationAsSkillAbsence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	failure := errors.New("source failed")
	source := skills.Merge(canceledSource{cancel: cancel, failure: errors.Join(skills.ErrSkillNotFound, failure)}, skills.Merge())
	_, err := source.OpenResource(ctx, "demo", "note")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) {
		t.Fatalf("canceled missing-skill lookup = %v", err)
	}
}
