package skills_test

import (
	"fmt"
	"slices"
	"testing/fstest"

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
