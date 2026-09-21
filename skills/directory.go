package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// rootedFS confines each open to its root without retaining a directory
// descriptor between calls. os.OpenInRoot also prevents symbolic links from
// escaping root, which os.DirFS deliberately does not guarantee.
type rootedFS string

func (r rootedFS) Open(name string) (fs.File, error) {
	return r.openInDir(".", name)
}

// confinedResourceFS lets a filesystem anchor resource symlink resolution at
// the selected skill rather than merely at the repository root.
type confinedResourceFS interface {
	openInDir(dir, name string) (fs.File, error)
}

func (r rootedFS) openInDir(dir, name string) (fs.File, error) {
	root, err := os.OpenRoot(string(r))
	if err != nil {
		return nil, err
	}
	sub, err := root.OpenRoot(filepath.FromSlash(dir))
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	file, err := sub.OpenFile(filepath.FromSlash(name), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	closeErr := errors.Join(sub.Close(), root.Close())
	if err != nil {
		return nil, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return nil, errors.Join(closeErr, file.Close())
	}
	return file, nil
}
