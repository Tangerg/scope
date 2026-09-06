package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Newly created workspace content is non-executable by default. The process
// umask may narrow these permissions further.
const (
	defaultDirectoryMode os.FileMode = 0o755
	defaultFileMode      os.FileMode = 0o644
)

// LocalExecutor is the reference local filesystem backend. Its constructor
// grants one immutable directory-tree authority; operation inputs may narrow
// that authority but cannot replace or escape it.
//
//   - Glob uses the platform-neutral doublestar matcher and never follows
//     directory symlinks while walking. Cancellation is checked between
//     filesystem operations even when the pattern has no matches.
//   - Grep consumes ripgrep's structured JSON protocol and returns
//     [ErrRipgrepUnavailable] when rg is not installed.
//   - Write and Edit serialize per file via [LocalExecutor.lockPath]
//     so concurrent tool calls on the same path can't tear.
//   - Read normalises CRLF→LF and strips UTF-8 BOM; Write and Edit
//     restore both when the existing file uses them.
type LocalExecutor struct {
	root string

	pathLocksMu sync.Mutex
	pathLocks   map[string]*pathLock
}

// NewLocalExecutor fixes one immutable directory-tree authority for every
// operation performed by the returned backend.
func NewLocalExecutor(root string) (*LocalExecutor, error) {
	if root == "" {
		return nil, ErrInvalidRoot
	}
	root = expandHome(root)
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fs.NewLocalExecutor: resolve root %q: %w", root, err)
	}
	return &LocalExecutor{root: filepath.Clean(absolute)}, nil
}

// authorize returns a root-relative path accepted by os.Root. Relative inputs
// must be local. Absolute inputs are accepted only when they are lexically
// beneath the immutable root, then reduced to the same relative identity.
func (l *LocalExecutor) authorize(path string, allowRoot bool) (string, error) {
	if l == nil {
		return "", ErrNilExecutor
	}
	if path == "" {
		if allowRoot {
			return ".", nil
		}
		return "", ErrEmptyPath
	}
	path = expandHome(path)
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(l.root, filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("fs: resolve %q beneath root: %w", path, err)
		}
		path = relative
	}
	path = filepath.Clean(path)
	if !filepath.IsLocal(path) || (!allowRoot && path == ".") {
		return "", fmt.Errorf("%w: %q", ErrPathOutsideRoot, path)
	}
	return path, nil
}

func (l *LocalExecutor) openRoot() (*os.Root, error) {
	if l == nil {
		return nil, ErrNilExecutor
	}
	root, err := os.OpenRoot(l.root)
	if err != nil {
		return nil, fmt.Errorf("fs: open executor root %q: %w", l.root, err)
	}
	return root, nil
}

// expandHome expands a leading ~ — the shell convention an LLM routinely emits
// — to the current user's home dir: "~" or "~/" → home, "~/x" → home/x. Any
// other form (a plain relative path, an absolute path, or "~user") is returned
// unchanged. Best-effort: if the home dir can't be resolved the path is left
// as-is. Without this, "~/x" anchors literally under Root as ".../~/x" and the
// open fails with "no such file or directory".
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[len("~/"):])
}

type pathLock struct {
	mutex sync.Mutex
	users int
}

// lockPath returns a per-path mutex unlock func. Entries are reference-counted
// and removed after the last holder/waiter leaves, so a long-running executor
// does not retain every path ever touched.
func (l *LocalExecutor) lockPath(path string) func() {
	l.pathLocksMu.Lock()
	if l.pathLocks == nil {
		l.pathLocks = map[string]*pathLock{}
	}
	entry, ok := l.pathLocks[path]
	if !ok {
		entry = &pathLock{}
		l.pathLocks[path] = entry
	}
	entry.users++
	l.pathLocksMu.Unlock()

	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		l.pathLocksMu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(l.pathLocks, path)
		}
		l.pathLocksMu.Unlock()
	}
}
