package fs_test

import (
	"fmt"

	toolfs "github.com/Tangerg/scope/tools/fs"
)

func ExampleNewReadTool() {
	executor, err := toolfs.NewLocalExecutor(".")
	if err != nil {
		panic(err)
	}
	defer executor.Close()
	read, err := toolfs.NewReadTool(executor)
	if err != nil {
		panic(err)
	}
	definition := read.Definition()

	fmt.Println(definition.Name)
	// Output:
	// read
}
