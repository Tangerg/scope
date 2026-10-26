package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	toolcontract "github.com/Tangerg/scope/core/tool"
	skillsrc "github.com/Tangerg/scope/skills"
)

type failingSource struct{ cause error }

func (f failingSource) List(context.Context) ([]skillsrc.Summary, error) { return nil, f.cause }

func (f failingSource) Load(context.Context, string) (*skillsrc.Skill, error) { return nil, f.cause }

func (f failingSource) OpenResource(context.Context, string, string) (fs.File, error) {
	return nil, f.cause
}

func TestSourceFailuresRemainIdentifiableAtEveryToolBoundary(t *testing.T) {
	cause := errors.New("source unavailable")
	built, err := NewTools(failingSource{cause: cause}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, executable := range built {
		arguments := `{}`
		switch executable.Definition().Name {
		case "load_skill":
			arguments = `{"name":"guide"}`
		case "read_skill_resource":
			arguments = `{"name":"guide","path":"ref.txt"}`
		}
		output, err := invokeTestTool(t.Context(), executable, arguments)
		if !errors.Is(err, cause) || testOutputText(output) != "" {
			t.Fatalf("%s = %v, %v", executable.Definition().Name, output, err)
		}
	}
}

func TestMalformedCursorIsRejectedBeforeSourceIO(t *testing.T) {
	built, err := NewTools(&panicSource{}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, executable := range built {
		members := ""
		switch executable.Definition().Name {
		case "load_skill":
			members = `"name":"guide",`
		case "read_skill_resource":
			members = `"name":"guide","path":"ref.txt",`
		}
		arguments := fmt.Sprintf(`{%s"cursor":{"offset":1,"snapshot":%q}}`, members, strings.Repeat("z", 64))
		_, err := invokeTestTool(t.Context(), executable, arguments)
		failure, definite := toolcontract.ClassifyFailure(err)
		if !definite || !errors.Is(failure.Cause(), ErrInvalidCursor) {
			t.Fatalf("%s = %v", executable.Definition().Name, err)
		}
	}
}

func TestCursorCannotContinueBeyondItsExactSnapshot(t *testing.T) {
	set := &toolSet{source: newToolFS(), maxOutputBytes: DefaultMaxOutputBytes, maxResourceBytes: skillsrc.DefaultMaxResourceBytes}
	for _, cursor := range []*Cursor{
		{Offset: 0, Snapshot: strings.Repeat("a", 64)},
		{Offset: 1, Snapshot: "short"},
	} {
		_, err := set.list(t.Context(), ListSkillsRequest{Cursor: cursor})
		assertCursorFailure(t, err)
	}
	_, err := set.list(t.Context(), ListSkillsRequest{Cursor: &Cursor{Offset: 100, Snapshot: strings.Repeat("a", 64)}})
	assertCursorFailure(t, err)
	_, err = set.readResource(t.Context(), ReadSkillResourceRequest{
		Name: "pdf-processing", Path: "references/REFERENCE.md", Cursor: &Cursor{Offset: 1, Snapshot: strings.Repeat("a", 64)},
	})
	assertCursorFailure(t, err)
}

func assertCursorFailure(t *testing.T, err error) {
	t.Helper()
	failure, definite := toolcontract.ClassifyFailure(err)
	if !definite || !errors.Is(failure.Cause(), ErrInvalidCursor) {
		t.Fatalf("cursor failure = %v", err)
	}
}

func TestInvalidLimitsCannotConstructTools(t *testing.T) {
	for _, config := range []Config{
		{MaxOutputBytes: -1},
		{MaxResourceBytes: -1},
		{MaxResourceBytes: maximumMaxOutputBytes + 1},
	} {
		if built, err := NewTools(newToolFS(), config); built != nil || !errors.Is(err, skillsrc.ErrInvalidLimit) {
			t.Fatalf("NewTools(%+v) = %v, %v", config, built, err)
		}
	}
}
