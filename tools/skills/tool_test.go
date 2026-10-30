package skills

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	toolcontract "github.com/Tangerg/scope/core/tool"
	skillsrc "github.com/Tangerg/scope/skills"
)

type panicSource struct{}

func (*panicSource) List(context.Context) ([]skillsrc.Summary, error) {
	panic("typed-nil source was used")
}

func (*panicSource) Load(context.Context, string) (*skillsrc.Skill, error) {
	panic("typed-nil source was used")
}

func (*panicSource) OpenResource(context.Context, string, string) (fs.File, error) {
	panic("typed-nil source was used")
}

func newToolFS() skillsrc.ResourceSource {
	repository, err := skillsrc.NewRepository(fstest.MapFS{
		"pdf-processing/SKILL.md":                {Data: []byte("---\nname: pdf-processing\ndescription: Handle PDFs.\n---\n# PDF\nDo the thing. See references/REFERENCE.md. This deliberately long instruction body verifies model-facing output limits.")},
		"pdf-processing/references/REFERENCE.md": {Data: []byte("detailed reference")},
		"pdf-processing/references/LONG.md":      {Data: []byte(strings.Repeat("reference ", 20))},
		"data-analysis/SKILL.md":                 {Data: []byte("---\nname: data-analysis\ndescription: Analyze data.\n---\nanalysis body")},
	}, skillsrc.RepositoryConfig{})
	if err != nil {
		panic(err)
	}
	return repository
}

func newTools(t *testing.T) map[string]toolcontract.Tool {
	return newToolsWithConfig(t, Config{})
}

func newToolsWithConfig(t *testing.T, config Config) map[string]toolcontract.Tool {
	t.Helper()
	built, err := NewTools(newToolFS(), config)
	if err != nil {
		t.Fatalf("NewTools: %v", err)
	}
	tools := make(map[string]toolcontract.Tool, len(built))
	for _, candidate := range built {
		tools[candidate.Definition().Name] = candidate
	}
	return tools
}

func TestNewToolsRejectsNilSource(t *testing.T) {
	var typedNil *panicSource
	for _, source := range []Source{nil, typedNil} {
		if _, err := NewTools(source, Config{}); !errors.Is(err, ErrNilSource) {
			t.Errorf("err = %v, want ErrNilSource", err)
		}
	}
}

func TestNewToolsBuildsOneStrictContractPerAction(t *testing.T) {
	tools := newTools(t)
	for _, name := range []string{"list_skills", "load_skill", "read_skill_resource"} {
		if tools[name] == nil {
			t.Errorf("missing %s: %v", name, tools)
		}
	}
	if len(tools) != 3 {
		t.Fatalf("tool names = %v, want exactly three", tools)
	}
	for name, candidate := range tools {
		if _, err := invokeTestTool(t.Context(), candidate, `{"op":"list"}`); err == nil {
			t.Errorf("%s accepted removed op argument", name)
		}
	}
}

func TestListSkills(t *testing.T) {
	output, err := invokeTestTool(t.Context(), newTools(t)["list_skills"], `{}`)
	if err != nil {
		t.Fatalf("list_skills: %v", err)
	}
	out := testOutputText(output)
	if !strings.Contains(out, `"name":"pdf-processing"`) || !strings.Contains(out, `"name":"data-analysis"`) {
		t.Errorf("list output missing skills:\n%s", out)
	}
}

func TestLoadSkill(t *testing.T) {
	output, err := invokeTestTool(t.Context(), newTools(t)["load_skill"], `{"name":"pdf-processing"}`)
	if err != nil {
		t.Fatalf("load_skill: %v", err)
	}
	out := testOutputText(output)
	if !strings.Contains(out, "Do the thing") {
		t.Errorf("load output missing instruction body:\n%s", out)
	}
}

func TestLoadSkillAcceptsFenceInsideYAMLBlockScalar(t *testing.T) {
	const instructions = "\n    printf 'first'\n\tprintf 'second'\nline with hard break  \nnext line\n\n"
	repository, err := skillsrc.NewRepository(fstest.MapFS{
		"formatting/SKILL.md": {Data: []byte("---\nname: formatting\ndescription: |\n  Before\n  ---\n  After\n---\n" + instructions)},
	}, skillsrc.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	built, err := NewTools(repository, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, executable := range built {
		if executable.Definition().Name != "load_skill" {
			continue
		}
		output, err := invokeTestTool(t.Context(), executable, `{"name":"formatting"}`)
		if err != nil {
			t.Fatal(err)
		}
		var page TextPage
		if err := jsonv2.Unmarshal([]byte(testOutputText(output)), &page); err != nil {
			t.Fatal(err)
		}
		if page.Content != instructions || page.NextCursor != nil {
			t.Fatalf("load_skill = %#v, want complete instructions %q", page, instructions)
		}
		return
	}
	t.Fatal("load_skill tool is missing")
}

func TestReadSkillResource(t *testing.T) {
	output, err := invokeTestTool(t.Context(), newTools(t)["read_skill_resource"], `{"name":"pdf-processing","path":"references/REFERENCE.md"}`)
	if err != nil {
		t.Fatalf("read_skill_resource: %v", err)
	}
	out := testOutputText(output)
	var page TextPage
	if err := jsonv2.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	if page.Content != "detailed reference" || page.NextCursor != nil {
		t.Errorf("resource content = %q", out)
	}
}

func TestNewToolsRejectsOutputLimitTooSmallForItsEnvelope(t *testing.T) {
	for _, maxOutputBytes := range []int64{minimumMaxOutputBytes - 1, maximumMaxOutputBytes + 1} {
		if _, err := NewTools(newToolFS(), Config{MaxOutputBytes: maxOutputBytes}); !errors.Is(err, skillsrc.ErrInvalidLimit) {
			t.Fatalf("NewTools(%d) error = %v, want ErrInvalidLimit", maxOutputBytes, err)
		}
	}
}

func TestReadSkillResourceRejectsInadmissibleRequestAsDefiniteFailure(t *testing.T) {
	// panicSource panics on any I/O, proving admission runs before the source is
	// touched. Both requests pass schema admission but fail the name/path rules.
	built, err := NewTools(&panicSource{}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	var readResource toolcontract.Tool
	for _, candidate := range built {
		if candidate.Definition().Name == "read_skill_resource" {
			readResource = candidate
		}
	}
	for _, arguments := range []string{
		`{"name":"pdf-processing","path":"../escape.md"}`,
		`{"name":"Bad Name","path":"ok.md"}`,
	} {
		_, callErr := invokeTestTool(t.Context(), readResource, arguments)
		failure, ok := toolcontract.ClassifyFailure(callErr)
		if !ok || failure.Kind() != toolcontract.FailureKindFailed {
			t.Fatalf("args %s: err = %v, want FailureKindFailed", arguments, callErr)
		}
	}
}

func TestLoadSkillRejectsInadmissibleNameAsDefiniteFailure(t *testing.T) {
	// panicSource panics on any I/O, proving the name rule runs before the source
	// is touched. "Bad Name" clears schema admission but fails ValidateName.
	built, err := NewTools(&panicSource{}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	var load toolcontract.Tool
	for _, candidate := range built {
		if candidate.Definition().Name == "load_skill" {
			load = candidate
		}
	}
	_, callErr := invokeTestTool(t.Context(), load, `{"name":"Bad Name"}`)
	failure, ok := toolcontract.ClassifyFailure(callErr)
	if !ok || failure.Kind() != toolcontract.FailureKindFailed || !errors.Is(failure.Cause(), skillsrc.ErrNameInvalid) {
		t.Fatalf("err = %v, want FailureKindFailed wrapping ErrNameInvalid", callErr)
	}
}

func TestSkillToolsRejectMissingArguments(t *testing.T) {
	tools := newTools(t)
	for _, test := range []struct {
		name      string
		arguments string
	}{
		{"load_skill", `{}`},
		{"load_skill", `{"name":""}`},
		{"read_skill_resource", `{"name":"pdf-processing"}`},
		{"read_skill_resource", `{"name":"pdf-processing","path":""}`},
	} {
		if _, err := invokeTestTool(t.Context(), tools[test.name], test.arguments); err == nil {
			t.Errorf("%s accepted %s", test.name, test.arguments)
		}
	}
}
