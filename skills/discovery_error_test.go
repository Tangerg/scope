package skills_test

import (
	"errors"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Tangerg/scope/skills"
	"github.com/Tangerg/scope/skills/internal/testfs"
)

var errFixtureClose = errors.New("injected skill file close failure")

type fixtureClosingFS struct{ skills.ResourceFS }
type fixtureClosingFile struct{ fs.File }

func (f fixtureClosingFS) Open(name string) (fs.File, error) {
	file, err := f.ResourceFS.Open(name)
	if err != nil {
		return nil, err
	}
	if name == "demo/SKILL.md" {
		return fixtureClosingFile{file}, nil
	}
	return file, nil
}

func (f fixtureClosingFile) Close() error { return errors.Join(f.File.Close(), errFixtureClose) }

func TestListPreservesIOError(t *testing.T) {
	fsys := fixtureClosingFS{testfs.FS{"demo/SKILL.md": &fstest.MapFile{Data: []byte("not frontmatter\n")}}}
	r, err := skills.NewRepository(fsys, skills.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.List(t.Context())
	if !errors.Is(err, errFixtureClose) {
		t.Fatalf("List discarded access failure: summaries=%v error=%v", got, err)
	}
}

type fixtureDirectoryReadErrorFS struct {
	skills.ResourceFS
	cause error
}

func (f fixtureDirectoryReadErrorFS) Open(name string) (fs.File, error) {
	file, err := f.ResourceFS.Open(name)
	if err != nil || name != "." {
		return file, err
	}
	return fixtureDirectoryReadErrorFile{ReadDirFile: file.(fs.ReadDirFile), cause: f.cause}, nil
}

type fixtureDirectoryReadErrorFile struct {
	fs.ReadDirFile
	cause error
}

func (f fixtureDirectoryReadErrorFile) ReadDir(count int) ([]fs.DirEntry, error) {
	entries, err := f.ReadDirFile.ReadDir(count)
	return entries, errors.Join(err, f.cause)
}

func TestListPreservesDirectoryReadErrorWithEntries(t *testing.T) {
	cause := errors.New("injected directory read failure")
	for _, test := range []struct {
		name  string
		fsys  skills.ResourceFS
		cause error
	}{
		{
			name:  "lookup also fails",
			fsys:  fixtureClosingFS{testfs.FS{"demo/SKILL.md": &fstest.MapFile{Data: []byte("not frontmatter\n")}}},
			cause: cause,
		},
		{
			name:  "read error includes EOF",
			fsys:  testfs.FS{"demo/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: demo\ndescription: Demo skill.\n---\n")}},
			cause: errors.Join(io.EOF, cause),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, err := skills.NewRepository(fixtureDirectoryReadErrorFS{ResourceFS: test.fsys, cause: test.cause}, skills.RepositoryConfig{})
			if err != nil {
				t.Fatal(err)
			}
			summaries, err := repository.List(t.Context())
			if !errors.Is(err, cause) || len(summaries) != 0 {
				t.Fatalf("List discarded directory read failure: summaries=%v error=%v", summaries, err)
			}
		})
	}
}

type fixtureContentReadErrorFS struct {
	skills.ResourceFS
	cause error
}

func (f fixtureContentReadErrorFS) Open(name string) (fs.File, error) {
	file, err := f.ResourceFS.Open(name)
	if err != nil || name != "demo/SKILL.md" {
		return file, err
	}
	return fixtureContentReadErrorFile{File: file, cause: f.cause}, nil
}

type fixtureContentReadErrorFile struct {
	fs.File
	cause error
}

func (f fixtureContentReadErrorFile) Read(data []byte) (int, error) {
	count, err := f.File.Read(data)
	if count > 0 {
		return count, errors.Join(err, f.cause)
	}
	return count, err
}

func TestRepositoryPreservesFrontmatterReadFailureWithContent(t *testing.T) {
	cause := errors.New("injected skill content read failure")
	for _, test := range []struct {
		name    string
		content string
		cause   error
	}{
		{"complete with newline", "---\nname: demo\ndescription: Demo skill.\n---\n", cause},
		{"complete without newline", "---\nname: demo\ndescription: Demo skill.\n---", errors.Join(io.EOF, cause)},
		{"incomplete", "---\nname: demo\ndescription: Demo skill.", errors.Join(io.EOF, cause)},
	} {
		for _, operation := range []string{"lookup", "list", "load"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				repository, err := skills.NewRepository(fixtureContentReadErrorFS{
					ResourceFS: testfs.FS{"demo/SKILL.md": &fstest.MapFile{Data: []byte(test.content)}}, cause: test.cause,
				}, skills.RepositoryConfig{})
				if err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "lookup":
					_, err = repository.Lookup(t.Context(), "demo")
				case "list":
					_, err = repository.List(t.Context())
				case "load":
					_, err = repository.Load(t.Context(), "demo")
				}
				if !errors.Is(err, cause) {
					t.Fatalf("%s lost skill content read failure: %v", operation, err)
				}
			})
		}
	}
}
