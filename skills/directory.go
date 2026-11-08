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

var _ ResourceFS = rootedFS{}

func (r rootedFS) Open(name string) (fs.File, error) {
	return r.OpenInDir(".", name)
}

func (r rootedFS) OpenInDir(dir, name string) (fs.File, error) {
	if !fs.ValidPath(dir) || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
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
