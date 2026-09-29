package fs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	slashpath "path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/Tangerg/scope/tools/textread"
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
//   - Glob uses the doublestar matcher and never follows directory symlinks.
//   - Grep streams files opened through os.Root to ripgrep. It skips hidden
//     entries and symlinks, uses doublestar path filters, and does not consult
//     ignore files. Missing rg returns [ErrRipgrepUnavailable].
//   - Mutations serialize within this executor and pin parent directories.
//     Directory aliases are supported; leaf symlinks are rejected.
//   - Read normalizes CRLF to LF and strips a UTF-8 BOM; Write and Edit
//     restore both when the existing file uses them.
type LocalExecutor struct {
	rootPath string
	root     *os.Root

	mutations sync.Mutex
}

// NewLocalExecutor derives an independent directory handle from root, never
// reopening its pathname, so host policies can inspect the same directory the
// executor uses even after its pathname is replaced.
//
// root must be open and have an absolute Name, which anchors absolute operation
// paths. The caller keeps ownership of root and must also Close the executor;
// closing either handle does not revoke the other. Sharing a root does not make
// separately issued host and executor operations atomic.
func NewLocalExecutor(root *os.Root) (*LocalExecutor, error) {
	if root == nil {
		return nil, ErrInvalidRoot
	}
	if !filepath.IsAbs(root.Name()) {
		return nil, fmt.Errorf("%w: root name must be absolute", ErrInvalidRoot)
	}
	directory, err := root.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("fs.NewLocalExecutor: acquire root %q: %w", root.Name(), err)
	}
	return &LocalExecutor{rootPath: filepath.Clean(root.Name()), root: directory}, nil
}

// Close prevents new operations. Operations that already acquired their own
// directory handle may finish independently.
func (l *LocalExecutor) Close() error { return l.root.Close() }

// authorize returns a root-relative path accepted by os.Root. Absolute inputs
// are accepted only when lexically beneath the root; os.Root then confines
// every symlink the relative path resolves through.
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
		relative, err := filepath.Rel(l.rootPath, filepath.Clean(path))
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
	root, err := l.root.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("fs: open executor root %q: %w", l.rootPath, err)
	}
	return root, nil
}

// Only ~ and ~/ expand. If home lookup fails, preserve the original path.
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

// Read does not serialize with mutations: atomic replacement means it observes
// either the complete previous file or the complete new one.
func (l *LocalExecutor) Read(ctx context.Context, in ReadInput) (_ ReadOutput, err error) {
	if err = in.validate(); err != nil {
		return ReadOutput{}, err
	}
	path, err := l.authorize(in.Path, false)
	if err != nil {
		return ReadOutput{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return ReadOutput{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	file, info, err := openRegularRootFile(ctx, root, path)
	if err != nil {
		return ReadOutput{}, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()

	limits := in.resolvedLimits()
	if info.Size() > limits.inputBytes {
		return ReadOutput{}, fmt.Errorf("%w: %s uses %d bytes; limit is %d", ErrFileTooLarge, in.Path, info.Size(), limits.inputBytes)
	}
	result, err := textread.Scan(ctx, file, textread.Options{
		InputBytes: limits.inputBytes, LineBytes: limits.lineBytes,
		OutputBytes: limits.outputBytes, StartLine: in.Offset, MaxLines: in.Limit,
		PartialLine: in.PartialLine,
	})
	if err != nil {
		return ReadOutput{}, limits.scanError(in.Path, err)
	}
	return ReadOutput{
		Content: result.Content, StartLine: result.StartLine, EndLine: result.EndLine,
		TotalLines: result.TotalLines, Truncated: result.Truncated,
	}, nil
}

func (l *LocalExecutor) Write(ctx context.Context, in WriteRequest) (_ WriteResponse, err error) {
	committing := false
	defer func() {
		if err != nil && !committing {
			err = fmt.Errorf("%w: %w", ErrMutationRejected, err)
		}
	}()
	if strings.ContainsRune(in.Content, 0) {
		return WriteResponse{}, fmt.Errorf("fs.LocalExecutor.Write: %w", ErrBinaryFile)
	}
	path, err := l.authorize(in.Path, false)
	if err != nil {
		return WriteResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return WriteResponse{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	l.mutations.Lock()
	defer l.mutations.Unlock()
	target, err := openMutationTarget(root, path, true)
	if err != nil {
		return WriteResponse{}, err
	}
	defer func() { err = errors.Join(err, target.parent.Close()) }()
	if cause := context.Cause(ctx); cause != nil {
		return WriteResponse{}, cause
	}

	var mode *os.FileMode
	hadBOM, hadCRLF := false, false
	if info, statErr := target.parent.Stat(target.name); statErr == nil {
		mode = new(info.Mode().Perm())
		hadBOM, hadCRLF, err = detectRootFormat(ctx, target.parent, target.name)
		if err != nil {
			return WriteResponse{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return WriteResponse{}, statErr
	}

	out := restoreFormat(in.Content, hadBOM, hadCRLF)
	committing = true
	if err := target.write(ctx, out, mode); err != nil {
		return WriteResponse{}, err
	}
	return WriteResponse{BytesWritten: len(out)}, nil
}

func (l *LocalExecutor) Edit(ctx context.Context, in EditRequest) (_ EditResponse, err error) {
	committing := false
	defer func() {
		if err != nil && !committing {
			err = fmt.Errorf("%w: %w", ErrMutationRejected, err)
		}
	}()
	path, err := l.authorize(in.Path, false)
	if err != nil {
		return EditResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return EditResponse{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	l.mutations.Lock()
	defer l.mutations.Unlock()
	target, err := openMutationTarget(root, path, false)
	if err != nil {
		return EditResponse{}, err
	}
	defer func() { err = errors.Join(err, target.parent.Close()) }()

	source, err := target.readText(ctx)
	if err != nil {
		return EditResponse{}, err
	}
	updated, replacements, err := (editOperation{
		OldString:  in.OldString,
		NewString:  in.NewString,
		ReplaceAll: in.ReplaceAll,
	}).apply(source.text, in.Path)
	if err != nil {
		return EditResponse{}, err
	}

	committing = true
	if err := target.write(ctx, source.restore(updated), &source.mode); err != nil {
		return EditResponse{}, err
	}
	return EditResponse{Replacements: replacements}, nil
}

func (l *LocalExecutor) Grep(ctx context.Context, in GrepInput) (_ GrepResponse, err error) {
	maxResults, err := in.resultLimit()
	if err != nil {
		return GrepResponse{}, err
	}
	mode, err := in.OutputMode.Normalize()
	if err != nil {
		return GrepResponse{}, fmt.Errorf("fs.LocalExecutor.Grep: %w", err)
	}
	base, err := l.authorize(in.Path, true)
	if err != nil {
		return GrepResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return GrepResponse{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	info, err := root.Stat(base)
	if err != nil {
		return GrepResponse{}, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return GrepResponse{}, fmt.Errorf("fs.LocalExecutor.Grep: %s: unsupported file mode %s", in.Path, info.Mode().Type())
	}
	executable, err := exec.LookPath(ripgrepExecutable)
	if err != nil {
		return GrepResponse{}, fmt.Errorf("fs.LocalExecutor.Grep: %w: %w", ErrRipgrepUnavailable, err)
	}
	search, err := newGrepSearch(ctx, root, executable, in, newRipgrepDecoder(mode, maxResults))
	if err != nil {
		return GrepResponse{}, fmt.Errorf("fs.LocalExecutor.Grep: %w", err)
	}
	if err = search.run(base, info.IsDir()); err != nil {
		return GrepResponse{}, fmt.Errorf("fs.LocalExecutor.Grep: %w", err)
	}
	return search.decoder.response, nil
}

func (l *LocalExecutor) Glob(ctx context.Context, in GlobRequest) (_ GlobResponse, err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return GlobResponse{}, contextErr
	}
	maxResults, err := in.resultLimit()
	if err != nil {
		return GlobResponse{}, err
	}
	base, err := l.authorize(in.Path, true)
	if err != nil {
		return GlobResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return GlobResponse{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	info, err := root.Stat(base)
	if err != nil {
		return GlobResponse{}, err
	}
	if !info.IsDir() {
		return GlobResponse{}, fmt.Errorf("fs.LocalExecutor.Glob: %s is not a directory", in.Path)
	}
	// Opening the base keeps its name out of the pattern, where glob
	// metacharacters in a directory name would select other directories.
	directory, err := root.OpenRoot(base)
	if err != nil {
		return GlobResponse{}, err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()

	options := []doublestar.GlobOption{
		doublestar.WithFilesOnly(),
		doublestar.WithNoFollow(),
		doublestar.WithFailOnIOErrors(),
	}
	if in.IgnoreCase {
		options = append(options, doublestar.WithCaseInsensitive())
	}
	matches := sortedPathSet{limit: maxResults}
	pattern := slashpath.Clean(filepath.ToSlash(in.Pattern))
	err = doublestar.GlobWalk(globFilesystem{FS: directory.FS(), ctx: ctx}, pattern, func(name string, _ fs.DirEntry) error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		matches.add(filepath.Join(base, filepath.FromSlash(name)))
		return nil
	}, options...)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return GlobResponse{}, fmt.Errorf("fs.LocalExecutor.Glob: %w", err)
	}
	return GlobResponse{Paths: matches.paths, Truncated: matches.truncated}, nil
}

func (l *LocalExecutor) ApplyPatch(ctx context.Context, in ApplyPatchRequest) (_ ApplyPatchResponse, err error) {
	committing := false
	defer func() {
		if err != nil && !committing {
			err = fmt.Errorf("%w: %w", ErrMutationRejected, err)
		}
	}()
	parsed, err := parseUnifiedPatch(in.Patch)
	if err != nil {
		return ApplyPatchResponse{}, err
	}
	resolved, err := l.resolvePatch(parsed)
	if err != nil {
		return ApplyPatchResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return ApplyPatchResponse{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	l.mutations.Lock()
	defer l.mutations.Unlock()
	targets := make(patchTargets)
	defer func() { err = errors.Join(err, targets.close()) }()
	if err = targets.open(root, resolved.files); err != nil {
		return ApplyPatchResponse{}, err
	}
	prepared := make([]preparedPatch, len(resolved.files))
	for i, file := range resolved.files {
		if prepared[i], err = file.prepare(ctx, targets); err != nil {
			return ApplyPatchResponse{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return ApplyPatchResponse{}, err
	}
	committing = true
	return commitPatches(ctx, prepared)
}

func commitPatches(ctx context.Context, prepared []preparedPatch) (ApplyPatchResponse, error) {
	var out ApplyPatchResponse
	for _, file := range prepared {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		result, err := file.commit(ctx)
		if result.Path != "" {
			out.Files = append(out.Files, result)
			out.Hunks += result.Hunks
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// Paths acquire their execution identity before duplicate detection, locking,
// preparation, or reporting. Those phases must agree on what one file means.
func (l *LocalExecutor) resolvePatch(patch unifiedPatch) (unifiedPatch, error) {
	resolved := unifiedPatch{files: make([]filePatch, len(patch.files))}
	for index, file := range patch.files {
		var err error
		if file.oldPath != "" {
			file.oldPath, err = l.authorize(file.oldPath, false)
			if err != nil {
				return unifiedPatch{}, err
			}
		}
		if file.newPath != "" {
			file.newPath, err = l.authorize(file.newPath, false)
			if err != nil {
				return unifiedPatch{}, err
			}
		}
		if err := file.validate(); err != nil {
			return unifiedPatch{}, err
		}
		resolved.files[index] = file
	}
	if err := resolved.validatePaths(); err != nil {
		return unifiedPatch{}, err
	}
	return resolved, nil
}
