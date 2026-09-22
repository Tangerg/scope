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

func ExampleApplyPatchTool_MutationPaths() {
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
	patch, err := toolfs.NewApplyPatchTool(executor)
	if err != nil {
		panic(err)
	}
	// Querying the prospective endpoints neither reads nor changes these files.
	paths, err := patch.MutationPaths([]byte(`{"patch":"diff --git a/old.txt b/new.txt\nsimilarity index 100%\nrename from old.txt\nrename to new.txt\n"}`))
	if err != nil {
		panic(err)
	}
	fmt.Println(paths)
	// Output:
	// [new.txt old.txt]
}
