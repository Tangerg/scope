package skills_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	skillsrc "github.com/Tangerg/scope/skills"
	skillstool "github.com/Tangerg/scope/tools/skills"
)

// ExampleNewTools exposes a skills source as three model-callable tools for
// progressive disclosure. The tool set is fixed regardless of the source's
// contents.
func ExampleNewTools() {
	source, err := skillsrc.NewRepository(fstest.MapFS{
		"pdf-processing/SKILL.md": {Data: []byte("---\nname: pdf-processing\ndescription: Handle PDFs.\n---\nInstruction body.")},
	}, skillsrc.RepositoryConfig{})
	if err != nil {
		panic(err)
	}
	built, err := skillstool.NewTools(source, skillstool.Config{})
	if err != nil {
		panic(err)
	}
	names := make([]string, len(built))
	for index, built := range built {
		names[index] = built.Definition().Name
	}
	slices.Sort(names)
	for _, name := range names {
		fmt.Println(name)
	}
	// Output:
	// list_skills
	// load_skill
	// read_skill_resource
}

func ExampleNewTools_pagination() {
	content := strings.Repeat("Complete instructions. ", 20)
	source, err := skillsrc.NewRepository(fstest.MapFS{
		"guide/SKILL.md": {Data: []byte("---\nname: guide\ndescription: Example guide.\n---\n" + content)},
	}, skillsrc.RepositoryConfig{})
	if err != nil {
		panic(err)
	}
	tools, err := skillstool.NewTools(source, skillstool.Config{MaxOutputBytes: 160})
	if err != nil {
		panic(err)
	}
	registry, err := tool.NewRegistry(tools...)
	if err != nil {
		panic(err)
	}
	binding, _ := registry.Resolve("load_skill")
	request := skillstool.LoadSkillRequest{Name: "guide"}
	var instructions strings.Builder
	for {
		arguments, err := jsonv2.Marshal(request)
		if err != nil {
			panic(err)
		}
		invocation, err := binding.Contract().Prepare(chat.ToolCall{
			ID: "read", Name: "load_skill", Arguments: string(arguments),
		}, nil)
		if err != nil {
			panic(err)
		}
		result, err := binding.Call(context.Background(), invocation)
		if err != nil {
			panic(err)
		}
		text, _ := result.Output.Text()
		var page skillstool.TextPage
		if err := jsonv2.Unmarshal([]byte(text), &page); err != nil {
			panic(err)
		}
		instructions.WriteString(page.Content)
		if page.NextCursor == nil {
			break
		}
		request.Cursor = page.NextCursor
	}
	fmt.Println(instructions.String() == content)
	// Output: true
}
