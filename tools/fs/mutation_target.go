package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func openMutationTarget(root *os.Root, path string, allowMissingParents bool) (_ *mutationTarget, err error) {
	directory := filepath.Dir(path)
	name := filepath.Base(path)
	var parent *os.Root
	for {
		parent, err = root.OpenRoot(directory)
		if err == nil {
			break
		}
		if !allowMissingParents || !errors.Is(err, os.ErrNotExist) || directory == "." {
			return nil, err
		}
		// A dangling link is not a missing directory we are allowed to create.
		if _, statErr := root.Lstat(directory); !errors.Is(statErr, os.ErrNotExist) {
			return nil, errors.Join(err, statErr)
		}
		name = filepath.Join(filepath.Base(directory), name)
		directory = filepath.Dir(directory)
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

func (m *mutationTarget) overlaps(other *mutationTarget) bool {
	return os.SameFile(m.directory, other.directory) &&
		(m.name == other.name || strings.HasPrefix(m.name, other.name+string(filepath.Separator)) ||
			strings.HasPrefix(other.name, m.name+string(filepath.Separator))) ||
		m.existing != nil && other.existing != nil && os.SameFile(m.existing, other.existing)
}

// Missing parents are created only at commit, beneath the existing directory
// pinned during preparation. Publishing still uses a pinned immediate parent.
func (m *mutationTarget) write(ctx context.Context, data []byte, preservedMode *os.FileMode) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	directory := filepath.Dir(m.name)
	if directory != "." {
		if err := m.parent.MkdirAll(directory, defaultDirectoryMode); err != nil {
			return err
		}
		parent, err := m.parent.OpenRoot(directory)
		if err != nil {
			return err
		}
		info, err := parent.Stat(".")
		if err != nil {
			return errors.Join(err, parent.Close())
		}
		previous := m.parent
		m.parent, m.directory, m.name = parent, info, filepath.Base(m.name)
		if err := previous.Close(); err != nil {
			return err
		}
	}
	if info, statErr := m.parent.Lstat(m.name); statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fs: %s: unsupported file mode %s", m.path, info.Mode().Type())
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	return atomicWriteRootFile(m.parent, m.name, data, preservedMode)
}
