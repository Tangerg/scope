package skills

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	toolcontract "github.com/Tangerg/scope/core/tool"
	skillsrc "github.com/Tangerg/scope/skills"
)

func pagingTools(t *testing.T, files fstest.MapFS, config Config) map[string]toolcontract.Tool {
	t.Helper()
	source, err := skillsrc.NewRepository(files, skillsrc.RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	built, err := NewTools(source, config)
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]toolcontract.Tool)
	for _, executable := range built {
		tools[executable.Definition().Name] = executable
	}
	return tools
}

func TestTextPagesPreserveEveryByteAndRespectEncodedBudget(t *testing.T) {
	content := strings.Repeat("中文😀\"\\\n\t", 200)
	files := fstest.MapFS{
		"guide/SKILL.md": {Data: []byte("---\nname: guide\ndescription: Guide.\n---\n" + content)},
		"guide/ref.txt":  {Data: []byte(content)},
	}
	const limit = 192
	tools := pagingTools(t, files, Config{MaxOutputBytes: limit})
	for _, name := range []string{"load_skill", "read_skill_resource"} {
		t.Run(name, func(t *testing.T) {
			var cursor *Cursor
			var full strings.Builder
			for pages := 0; ; pages++ {
				if pages > len(content) {
					t.Fatal("pagination did not finish")
				}
				arguments := struct {
					Name   string  `json:"name"`
					Path   string  `json:"path,omitzero"`
					Cursor *Cursor `json:"cursor,omitzero"`
				}{Name: "guide", Cursor: cursor}
				if name == "read_skill_resource" {
					arguments.Path = "ref.txt"
				}
				data, err := jsonv2.Marshal(arguments)
				if err != nil {
					t.Fatal(err)
				}
				output, err := invokeTestTool(t.Context(), tools[name], string(data))
				if err != nil {
					t.Fatal(err)
				}
				text := testOutputText(output)
				if len(text) > limit || !utf8.ValidString(text) {
					t.Fatalf("invalid bounded page: %q", text)
				}
				var page TextPage
				if err := jsonv2.Unmarshal([]byte(text), &page); err != nil {
					t.Fatal(err)
				}
				full.WriteString(page.Content)
				if page.NextCursor == nil {
					break
				}
				if page.Content == "" || (cursor != nil && page.NextCursor.Offset <= cursor.Offset) {
					t.Fatal("cursor did not advance")
				}
				cursor = page.NextCursor
			}
			if full.String() != content {
				t.Fatalf("paged content differs: got %d bytes, want %d", full.Len(), len(content))
			}
		})
	}
}

func TestListPagesKeepEveryCompleteSummary(t *testing.T) {
	files := fstest.MapFS{}
	for index := range 12 {
		name := fmt.Sprintf("skill-%02d", index)
		files[name+"/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: " + name + "\ndescription: " + strings.Repeat("description ", 5) + "\n---\nbody")}
	}
	const limit = 270
	tools := pagingTools(t, files, Config{MaxOutputBytes: limit})
	request := ListSkillsRequest{}
	seen := make(map[string]bool)
	for pages := 0; ; pages++ {
		if pages > len(files) {
			t.Fatal("listing did not finish")
		}
		arguments, err := jsonv2.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		output, err := invokeTestTool(t.Context(), tools["list_skills"], string(arguments))
		if err != nil {
			t.Fatal(err)
		}
		text := testOutputText(output)
		if len(text) > limit {
			t.Fatalf("page exceeds %d bytes", limit)
		}
		var page ListPage
		if err := jsonv2.Unmarshal([]byte(text), &page); err != nil {
			t.Fatal(err)
		}
		for _, summary := range page.Skills {
			if err := summary.Validate(); err != nil {
				t.Fatal(err)
			}
			if seen[summary.Name] {
				t.Fatalf("duplicate %s", summary.Name)
			}
			seen[summary.Name] = true
		}
		if page.NextCursor == nil {
			break
		}
		if len(page.Skills) == 0 {
			t.Fatal("empty nonfinal page")
		}
		request.Cursor = page.NextCursor
	}
	if len(seen) != len(files) {
		t.Fatalf("listed %d of %d skills", len(seen), len(files))
	}
}

func TestCursorBindsContentTargetAndUTF8Boundary(t *testing.T) {
	content := strings.Repeat("中文", 100)
	first, err := textPage("resource", "guide", "a.txt", content, nil, 160)
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page: %v %v", first, err)
	}
	for _, test := range []struct{ operation, name, path, content string }{
		{"resource", "guide", "a.txt", content + "changed"},
		{"resource", "guide", "b.txt", content},
		{"resource", "other", "a.txt", content},
		{"load", "guide", "a.txt", content},
	} {
		_, err := textPage(test.operation, test.name, test.path, test.content, first.NextCursor, 160)
		if !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("unbound cursor accepted: %v", err)
		}
	}
	invalid := *first.NextCursor
	invalid.Offset = 1
	if _, err := textPage("resource", "guide", "a.txt", content, &invalid, 160); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("split UTF-8 cursor: %v", err)
	}
}

func TestSkillPageFailuresAreExplicit(t *testing.T) {
	files := fstest.MapFS{
		"guide/SKILL.md": {Data: []byte("---\nname: guide\ndescription: " + strings.Repeat("long ", 100) + "\n---\nbody")},
		"guide/ref.txt":  {Data: []byte(strings.Repeat("a", 1000))},
		"guide/binary":   {Data: []byte{0xff, 0xfe}},
	}
	tools := pagingTools(t, files, Config{MaxOutputBytes: 160, MaxResourceBytes: 100})
	for _, test := range []struct {
		name, args string
		cause      error
	}{
		{"list_skills", `{}`, ErrOutputLimit},
		{"read_skill_resource", `{"name":"guide","path":"ref.txt"}`, skillsrc.ErrInvalidLimit},
		{"read_skill_resource", `{"name":"guide","path":"binary"}`, skillsrc.ErrInvalidSkill},
		{"load_skill", `{"name":"guide","cursor":{"offset":1,"snapshot":"` + strings.Repeat("a", 64) + `"}}`, ErrInvalidCursor},
	} {
		_, err := invokeTestTool(t.Context(), tools[test.name], test.args)
		failure, ok := toolcontract.ClassifyFailure(err)
		if !ok || !errors.Is(failure.Cause(), test.cause) {
			t.Fatalf("%s: %v", test.name, err)
		}
	}
}

func TestPageAtExactEncodedLimitIsComplete(t *testing.T) {
	content := strings.Repeat("text\"", 40)
	encoded, err := jsonv2.Marshal(TextPage{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	page, err := textPage("load", "guide", "", content, nil, int64(len(encoded)))
	if err != nil || page.NextCursor != nil || page.Content != content {
		t.Fatalf("exact limit: %v %v", page, err)
	}
}
