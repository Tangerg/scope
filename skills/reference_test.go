package skills_test

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Tangerg/scope/skills"
)

func TestExactContentCannotFollowChangedOrMissingResource(t *testing.T) {
	files := fstest.MapFS{"guide/SKILL.md": {Data: []byte("---\nname: guide\ndescription: Guide.\n---\nbody")}, "guide/ref.txt": {Data: []byte("first")}}
	source, err := skills.NewRepository(files, skills.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, content string }{{"", "body"}, {"ref.txt", "first"}} {
		ref, newReferenceErr := skills.NewReference("guide", tc.path, []byte(tc.content))
		if newReferenceErr != nil {
			t.Fatal(newReferenceErr)
		}
		b, newReferenceErr := ref.Resolve(t.Context(), source, 256)
		if newReferenceErr != nil || string(b) != tc.content {
			t.Fatalf("resolve: %q %v", b, newReferenceErr)
		}
	}
	ref, err := skills.NewReference("guide", "ref.txt", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	files["guide/ref.txt"].Data = []byte("other")
	if b, err := ref.Resolve(t.Context(), source, 256); b != nil || !errors.Is(err, skills.ErrUnresolvedReference) {
		t.Fatalf("changed: %q %v", b, err)
	}
	delete(files, "guide/ref.txt")
	if b, err := ref.Resolve(t.Context(), source, 256); b != nil || !errors.Is(err, skills.ErrUnresolvedReference) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed: %q %v", b, err)
	}
}
