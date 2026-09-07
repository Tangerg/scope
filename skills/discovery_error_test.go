package skills_test

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Tangerg/scope/skills"
)

var errFixtureClose = errors.New("injected skill file close failure")

type fixtureClosingFS struct{ fs.FS }
type fixtureClosingFile struct{ fs.File }

func (f fixtureClosingFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
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
	fsys := fixtureClosingFS{fstest.MapFS{"demo/SKILL.md": &fstest.MapFile{Data: []byte("not frontmatter\n")}}}
	r, err := skills.NewRepository(fsys, skills.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.List(t.Context())
	if !errors.Is(err, errFixtureClose) {
		t.Fatalf("List discarded access failure: summaries=%v error=%v", got, err)
	}
}
