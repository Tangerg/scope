// Package testfs supplies an in-memory resource filesystem for skill tests.
package testfs

import (
	"io/fs"
	"path"
	"testing/fstest"
)

// FS never resolves links or accesses the host filesystem. OpenInDir rejects
// symbolic-link entries even when their descendants appear in the fixture map.
type FS fstest.MapFS

func (f FS) Open(name string) (fs.File, error) {
	return fstest.MapFS(f).Open(name)
}

func (f FS) OpenInDir(dir, name string) (fs.File, error) {
	if !fs.ValidPath(dir) || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	full := path.Join(dir, name)
	for current := full; current != "."; current = path.Dir(current) {
		if entry := f[current]; entry != nil && entry.Mode&fs.ModeSymlink != 0 {
			return nil, &fs.PathError{Op: "open", Path: full, Err: fs.ErrPermission}
		}
	}
	return f.Open(full)
}
