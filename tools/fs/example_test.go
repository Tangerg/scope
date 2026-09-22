package fs_test

import (
	"fmt"
	"os"

	toolfs "github.com/Tangerg/scope/tools/fs"
)

func ExampleNewReadTool() {
	path, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		panic(err)
	}
	defer root.Close()
	executor, err := toolfs.NewLocalExecutor(root)
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
