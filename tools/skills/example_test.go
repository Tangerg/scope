package skills_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/tool"
	skillsrc "github.com/Tangerg/scope/skills"
	skillstool "github.com/Tangerg/scope/tools/skills"
)

func ExampleNewTools() {
	root, err := os.OpenRoot("testdata")
	if err != nil {
		panic(err)
	}
	defer root.Close()
	source, err := skillsrc.NewDirectoryRepository(root, skillsrc.RepositoryConfig{})
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
	root, err := os.OpenRoot("testdata")
	if err != nil {
		panic(err)
	}
	defer root.Close()
	source, err := skillsrc.NewDirectoryRepository(root, skillsrc.RepositoryConfig{})
	if err != nil {
		panic(err)
	}
	tools, err := skillstool.NewTools(source, skillstool.Config{MaxOutputBytes: 160})
	if err != nil {
		panic(err)
	}
	bindings := make([]tool.Binding, 0, len(tools))
	for _, executable := range tools {
		binding, bindErr := tool.Bind(executable)
		if bindErr != nil {
			panic(bindErr)
		}
		bindings = append(bindings, binding)
	}
	registry, err := tool.NewRegistry(bindings...)
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
		invocation, err := binding.Contract().Prepare(tool.Request{Name: "load_skill", Arguments: string(arguments)}, tool.Admission{})
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
	fmt.Println(instructions.String() == strings.Repeat("Complete instructions.\n", 20))
	// Output: true
}
