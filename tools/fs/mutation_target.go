package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A mutation owns a directory entry, not a pathname that can be redirected
// between reading and publishing. The parent remains open through commit.
type mutationTarget struct {
	parent    *os.Root
	directory os.FileInfo
	name      string
	path      string
	existing  os.FileInfo
}

func openMutationTarget(root *os.Root, path string, createParents bool) (_ *mutationTarget, err error) {
	directory := filepath.Dir(path)
	if createParents {
		if mkdirErr := root.MkdirAll(directory, defaultDirectoryMode); mkdirErr != nil {
			return nil, mkdirErr
		}
	}
	parent, err := root.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, parent.Close())
		}
	}()
	info, err := parent.Stat(".")
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	existing, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if existing != nil && !existing.Mode().IsRegular() {
		return nil, fmt.Errorf("fs: %s: unsupported file mode %s", path, existing.Mode().Type())
	}
	return &mutationTarget{parent: parent, directory: info, name: name, path: path, existing: existing}, nil
}

func (m *mutationTarget) same(other *mutationTarget) bool {
	return os.SameFile(m.directory, other.directory) && m.name == other.name ||
		m.existing != nil && other.existing != nil && os.SameFile(m.existing, other.existing)
}
