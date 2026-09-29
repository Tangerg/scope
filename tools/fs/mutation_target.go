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
	parent, name, err := openNearestParent(root, path, allowMissingParents)
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
	existing, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		existing, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existing != nil && !existing.Mode().IsRegular() {
		return nil, fmt.Errorf("fs: %s: unsupported file mode %s", path, existing.Mode().Type())
	}
	return &mutationTarget{parent: parent, directory: info, name: name, path: path, existing: existing}, nil
}

// openNearestParent pins the closest existing ancestor when parents may be
// created, returning the remaining path beneath it as the entry name.
func openNearestParent(root *os.Root, path string, allowMissingParents bool) (*os.Root, string, error) {
	directory, name := filepath.Dir(path), filepath.Base(path)
	for {
		parent, err := root.OpenRoot(directory)
		if err == nil {
			return parent, name, nil
		}
		if !allowMissingParents || !errors.Is(err, os.ErrNotExist) || directory == "." {
			return nil, "", err
		}
		// A dangling link is not a missing directory we are allowed to create.
		if _, statErr := root.Lstat(directory); !errors.Is(statErr, os.ErrNotExist) {
			return nil, "", errors.Join(err, statErr)
		}
		name = filepath.Join(filepath.Base(directory), name)
		directory = filepath.Dir(directory)
	}
}

func (m *mutationTarget) overlaps(other *mutationTarget) bool {
	if m.existing != nil && other.existing != nil && os.SameFile(m.existing, other.existing) {
		return true
	}
	if !os.SameFile(m.directory, other.directory) {
		return false
	}
	separator := string(filepath.Separator)
	return m.name == other.name ||
		strings.HasPrefix(m.name, other.name+separator) ||
		strings.HasPrefix(other.name, m.name+separator)
}

func (m *mutationTarget) requireAbsent() error {
	_, err := m.parent.Stat(m.name)
	if err == nil {
		return fmt.Errorf("fs.ApplyPatch: %s: file already exists", m.path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs.ApplyPatch: %s: %w", m.path, err)
	}
	return nil
}

func (m *mutationTarget) readText(ctx context.Context) (sourceText, error) {
	info, err := m.parent.Stat(m.name)
	if err != nil {
		return sourceText{}, err
	}
	data, err := readBoundedRootFile(ctx, m.parent, m.name, defaultMutationInputBytes)
	if err != nil {
		return sourceText{}, err
	}
	if looksBinary(data) {
		return sourceText{}, ErrBinaryFile
	}
	text, hadBOM, hadCRLF := normalizeText(data)
	return sourceText{text: text, mode: info.Mode().Perm(), hadBOM: hadBOM, hadCRLF: hadCRLF}, nil
}

// Missing parents are created only at commit, beneath the existing directory
// pinned during preparation. Publishing still uses a pinned immediate parent.
func (m *mutationTarget) write(ctx context.Context, data []byte, preservedMode *os.FileMode) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err := m.createParents(); err != nil {
		return err
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

func (m *mutationTarget) createParents() error {
	directory := filepath.Dir(m.name)
	if directory == "." {
		return nil
	}
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
	return previous.Close()
}

type sourceText struct {
	text    string
	mode    os.FileMode
	hadBOM  bool
	hadCRLF bool
}

func (s sourceText) restore(updated string) []byte {
	return restoreFormat(updated, s.hadBOM, s.hadCRLF)
}
