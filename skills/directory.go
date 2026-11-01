package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// rootedFS borrows the authority granted by an open directory. A temporary
// subroot confines relative resource symlinks to their selected skill.
type rootedFS struct{ root *os.Root }

func (r rootedFS) Open(name string) (fs.File, error) {
	return r.openInDir(".", name)
}

// confinedResourceFS lets a filesystem anchor resource symlink resolution at
// the selected skill rather than merely at the repository root.
type confinedResourceFS interface {
	openInDir(dir, name string) (fs.File, error)
}

func (r rootedFS) openInDir(dir, name string) (fs.File, error) {
	sub, err := r.root.OpenRoot(filepath.FromSlash(dir))
	if err != nil {
		return nil, err
	}
	file, err := sub.OpenFile(filepath.FromSlash(name), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	closeErr := sub.Close()
	if err != nil {
		return nil, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return nil, errors.Join(closeErr, file.Close())
	}
	return file, nil
}
