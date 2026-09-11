package skills_test

import (
	"context"
	"fmt"
	"testing/fstest"

	"github.com/Tangerg/scope/skills"
)

func ExampleNewRepository() {
	repository, err := skills.NewRepository(fstest.MapFS{
		"review/SKILL.md": {Data: []byte("---\nname: review\ndescription: Review code.\n---\nRead the code before suggesting changes.")},
	}, skills.RepositoryConfig{})
	if err != nil {
		panic(err)
	}
	// Lookup checks metadata for an already known name without reading its body.
	if _, lookupErr := repository.Lookup(context.Background(), "review"); lookupErr != nil {
		panic(lookupErr)
	}
	summaries, err := repository.List(context.Background())
	if err != nil {
		panic(err)
	}
	skill, err := repository.Load(context.Background(), summaries[0].Name)
	if err != nil {
		panic(err)
	}

	fmt.Println(skill.Name, skill.Instructions)
	// Output:
	// review Read the code before suggesting changes.
}
