package skills

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	toolcontract "github.com/Tangerg/scope/core/tool"
	skillsrc "github.com/Tangerg/scope/skills"
)

type panicSource struct{}

func (*panicSource) Lookup(context.Context, string) (skillsrc.Summary, error) {
	panic("unexpected Lookup")
}

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
	for _, source := range []skillsrc.ResourceSource{nil, typedNil} {
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
	if !strings.Contains(out, "<name>pdf-processing</name>") || !strings.Contains(out, "<name>data-analysis</name>") {
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

func TestReadSkillResource(t *testing.T) {
	output, err := invokeTestTool(t.Context(), newTools(t)["read_skill_resource"], `{"name":"pdf-processing","path":"references/REFERENCE.md"}`)
	if err != nil {
		t.Fatalf("read_skill_resource: %v", err)
	}
	out := testOutputText(output)
	if out != "detailed reference" {
		t.Errorf("resource content = %q", out)
	}
}

func TestSkillToolsBoundModelFacingContent(t *testing.T) {
	const maxOutputBytes = int64(68)
	tools := newToolsWithConfig(t, Config{MaxOutputBytes: maxOutputBytes})

	loadedOutput, err := invokeTestTool(t.Context(), tools["load_skill"], `{"name":"pdf-processing"}`)
	if err != nil {
		t.Fatal(err)
	}
	loaded := testOutputText(loadedOutput)
	if int64(len(loaded)) > maxOutputBytes || !strings.HasSuffix(loaded, "[output truncated]") || !strings.HasPrefix(loaded, "# PDF") {
		t.Fatalf("bounded skill output = %q", loaded)
	}

	resourceOutput, err := invokeTestTool(t.Context(), tools["read_skill_resource"], `{"name":"pdf-processing","path":"references/LONG.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	resource := testOutputText(resourceOutput)
	if int64(len(resource)) > maxOutputBytes || !strings.HasSuffix(resource, "[output truncated]") {
		t.Fatalf("bounded resource output = %q", resource)
	}

	listedOutput, err := invokeTestTool(t.Context(), tools["list_skills"], `{}`)
	if err != nil {
		t.Fatal(err)
	}
	listed := testOutputText(listedOutput)
	if !strings.Contains(listed, "<truncated>true</truncated>") {
		t.Fatalf("bounded list output = %q", listed)
	}
	if int64(len(listed)) > maxOutputBytes {
		t.Fatalf("list output length = %d, want <= %d", len(listed), maxOutputBytes)
	}
}

func TestNewToolsRejectsOutputLimitTooSmallForItsEnvelope(t *testing.T) {
	for _, maxOutputBytes := range []int64{minimumMaxOutputBytes - 1, maximumMaxOutputBytes + 1} {
		if _, err := NewTools(newToolFS(), Config{MaxOutputBytes: maxOutputBytes}); !errors.Is(err, skillsrc.ErrInvalidLimit) {
			t.Fatalf("NewTools(%d) error = %v, want ErrInvalidLimit", maxOutputBytes, err)
		}
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
