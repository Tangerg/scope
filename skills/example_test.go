package skills_test

import (
	"context"
	"fmt"
	"os"

	"github.com/Tangerg/scope/skills"
)

func ExampleNewDirectoryRepository() {
	root, err := os.OpenRoot("testdata")
	if err != nil {
		panic(err)
	}
	defer root.Close()
	repository, err := skills.NewDirectoryRepository(root, skills.RepositoryConfig{})
	if err != nil {
		panic(err)
	}
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
