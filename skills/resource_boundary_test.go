package skills_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tangerg/scope/skills"
	"github.com/Tangerg/scope/skills/internal/testfs"
)

type resourceCountingFS struct {
	skills.ResourceFS
	opens map[string]int
	stats map[string]int
}

func (r *resourceCountingFS) Open(name string) (fs.File, error) {
	file, err := r.ResourceFS.Open(name)
	return r.record(name, file, err)
}

func (r *resourceCountingFS) OpenInDir(dir, name string) (fs.File, error) {
	file, err := r.ResourceFS.OpenInDir(dir, name)
	return r.record(dir+"/"+name, file, err)
}

func (r *resourceCountingFS) record(name string, file fs.File, err error) (fs.File, error) {
	r.opens[name]++
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

func TestOverlaidResourceReadsOwningSkillAndChecksFileOnce(t *testing.T) {
	backing := &resourceCountingFS{
		ResourceFS: testfs.FS{
			"demo/SKILL.md": {Data: []byte("---\nname: demo\ndescription: Demonstration\n---\nInstructions")},
			"demo/note":     {Data: []byte("resource")},
		},
		opens: map[string]int{}, stats: map[string]int{},
	}
	repository, err := skills.NewRepository(backing, skills.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	data, truncated, err := skills.ReadResource(t.Context(), skills.Overlay(skills.Overlay(repository)), "demo", "note", 1024)
	if err != nil || truncated || string(data) != "resource" {
		t.Fatalf("ReadResource = %q, %t, %v", data, truncated, err)
	}
	if backing.opens["demo/SKILL.md"] != 1 || backing.opens["demo/note"] != 1 || backing.stats["demo/note"] != 1 {
		t.Fatalf("one resource operation repeated filesystem work: opens %v, stats %v", backing.opens, backing.stats)
	}
}

func TestResourceAbsenceRetainsSkillOwnership(t *testing.T) {
	repository, err := skills.NewRepository(testfs.FS{
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

func TestOverlayDoesNotHideCancellationAsSkillAbsence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	failure := errors.New("source failed")
	source := skills.Overlay(canceledSource{cancel: cancel, failure: errors.Join(skills.ErrSkillNotFound, failure)}, skills.Overlay())
	_, err := source.OpenResource(ctx, "demo", "note")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) {
		t.Fatalf("canceled missing-skill lookup = %v", err)
	}
}

type resourceDeniedFS struct {
	fs.FS
	resourceOpens int
}

func (r *resourceDeniedFS) OpenInDir(string, string) (fs.File, error) {
	r.resourceOpens++
	return nil, fs.ErrPermission
}

func TestRepositoryCannotBypassResourceAuthorityThroughFilesystemOpen(t *testing.T) {
	directory := t.TempDir()
	bundle := filepath.Join(directory, "guide")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, skills.SkillFile), []byte("---\nname: guide\ndescription: Guide.\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside the repository"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(bundle, "secret")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	backend := &resourceDeniedFS{FS: os.DirFS(directory)}
	repository, err := skills.NewRepository(backend, skills.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	data, truncated, err := skills.ReadResource(t.Context(), skills.Overlay(repository), "guide", "secret", 128)
	if !errors.Is(err, fs.ErrPermission) || len(data) != 0 || truncated || backend.resourceOpens != 1 {
		t.Fatalf("resource authority was bypassed: data=%q truncated=%t calls=%d error=%v", data, truncated, backend.resourceOpens, err)
	}
}
