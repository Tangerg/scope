package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	slashpath "path"
	"path/filepath"
	"slices"
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
//   - Glob uses the platform-neutral doublestar matcher and never follows
//     directory symlinks while walking. Cancellation is checked between
//     filesystem operations even when the pattern has no matches.
//   - Grep opens files through os.Root and feeds their contents to ripgrep.
//     It skips hidden entries and symlinks during traversal, uses doublestar
//     path filters, and does not consult ignore files. Missing rg returns
//     [ErrRipgrepUnavailable].
//   - Mutations serialize within this executor and pin parent directories.
//     Directory aliases are supported; leaf symlinks are rejected.
//   - Read normalises CRLF→LF and strips UTF-8 BOM; Write and Edit
//     restore both when the existing file uses them.
type LocalExecutor struct {
	rootPath string
	root     *os.Root

	mutations sync.Mutex
}

// NewLocalExecutor fixes one immutable directory-tree authority for every
// operation performed by the returned backend. The directory must exist and
// remains the authority even if its original path is renamed or replaced.
// The caller must Close the executor when it is no longer needed.
func NewLocalExecutor(root string) (*LocalExecutor, error) {
	if root == "" {
		return nil, ErrInvalidRoot
	}
	root = expandHome(root)
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fs.NewLocalExecutor: resolve root %q: %w", root, err)
	}
	directory, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("fs.NewLocalExecutor: open root %q: %w", absolute, err)
	}
	return &LocalExecutor{rootPath: filepath.Clean(absolute), root: directory}, nil
}

// Close releases the directory authority and prevents new operations. Operations
// that already acquired their own directory handle may finish independently.
func (l *LocalExecutor) Close() error { return l.root.Close() }

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

// Read does not lock — concurrent reads are fine and a slightly stale
// read while another goroutine writes is acceptable (atomic-rename in
// Write means the caller sees either the old file in full or the new file in
// full, never a torn write).
func (l *LocalExecutor) Read(ctx context.Context, in ReadInput) (_ ReadOutput, err error) {
	if in.Limit < 0 || in.MaxInputBytes < 0 || in.MaxLineBytes < 0 || in.MaxOutputBytes < 0 {
		return ReadOutput{}, fmt.Errorf("%w: read limits must not be negative", ErrInvalidInput)
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
		switch {
		case errors.Is(err, textread.ErrInputTooLarge):
			return ReadOutput{}, fmt.Errorf("%w: %s grew beyond %d bytes", ErrFileTooLarge, in.Path, limits.inputBytes)
		case errors.Is(err, textread.ErrLineTooLarge):
			return ReadOutput{}, &lineLimitError{
				path: in.Path, line: textread.LineNumber(err), limit: limits.lineBytes,
			}
		case errors.Is(err, textread.ErrInvalidText):
			return ReadOutput{}, ErrBinaryFile
		default:
			return ReadOutput{}, fmt.Errorf("fs.LocalExecutor.Read: scan %s: %w", in.Path, err)
		}
	}

	return ReadOutput{
		Content: result.Content, StartLine: result.StartLine, EndLine: result.EndLine,
		TotalLines: result.TotalLines, Truncated: result.Truncated,
	}, nil
}

func (l *LocalExecutor) Write(ctx context.Context, in WriteRequest) (_ WriteResponse, err error) {
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
	defer func(authority *os.Root) {
		err = errors.Join(err, authority.Close())
	}(root)

	l.mutations.Lock()
	defer l.mutations.Unlock()
	target, err := openMutationTarget(root, path, true)
	if err != nil {
		return WriteResponse{}, err
	}
	defer func() { err = errors.Join(err, target.parent.Close()) }()
	root, path = target.parent, target.name
	if cause := context.Cause(ctx); cause != nil {
		return WriteResponse{}, cause
	}

	// Detect existing format + permissions so an overwrite preserves
	// CRLF / BOM / mode instead of silently flipping them.
	var mode *os.FileMode
	hadBOM, hadCRLF := false, false
	if info, statErr := root.Stat(path); statErr == nil {
		mode = new(info.Mode().Perm())
		hadBOM, hadCRLF, err = detectRootFormat(ctx, root, path)
		if err != nil {
			return WriteResponse{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return WriteResponse{}, statErr
	}

	out := restoreFormat(in.Content, hadBOM, hadCRLF)
	if err := target.write(ctx, out, mode); err != nil {
		return WriteResponse{}, err
	}
	return WriteResponse{BytesWritten: len(out)}, nil
}

func (l *LocalExecutor) Edit(ctx context.Context, in EditRequest) (_ EditResponse, err error) {
	path, err := l.authorize(in.Path, false)
	if err != nil {
		return EditResponse{}, err
	}
	root, err := l.openRoot()
	if err != nil {
		return EditResponse{}, err
	}
	defer func(authority *os.Root) {
		err = errors.Join(err, authority.Close())
	}(root)

	l.mutations.Lock()
	defer l.mutations.Unlock()
	target, err := openMutationTarget(root, path, false)
	if err != nil {
		return EditResponse{}, err
	}
	defer func() { err = errors.Join(err, target.parent.Close()) }()
	root, path = target.parent, target.name

	data, err := readBoundedRootFile(ctx, root, path, defaultMutationInputBytes)
	if err != nil {
		return EditResponse{}, err
	}
	if looksBinary(data) {
		return EditResponse{}, fmt.Errorf("%w: %w", ErrEditRejected, ErrBinaryFile)
	}

	content, hadBOM, hadCRLF := normalizeText(data)
	updated, replacements, err := (editOperation{
		OldString:  in.OldString,
		NewString:  in.NewString,
		ReplaceAll: in.ReplaceAll,
	}).apply(content, in.Path)
	if err != nil {
		return EditResponse{}, fmt.Errorf("%w: %w", ErrEditRejected, err)
	}

	info, err := root.Stat(path)
	if err != nil {
		return EditResponse{}, fmt.Errorf("fs: stat edited file %q: %w", in.Path, err)
	}
	mode := new(info.Mode().Perm())

	out := restoreFormat(updated, hadBOM, hadCRLF)
	if err := target.write(ctx, out, mode); err != nil {
		return EditResponse{}, err
	}
	return EditResponse{Replacements: replacements}, nil
}

func (l *LocalExecutor) Grep(ctx context.Context, in GrepInput) (_ GrepResponse, err error) {
	if in.MaxResults < 0 || in.Context < 0 || in.BeforeContext < 0 || in.AfterContext < 0 ||
		in.Context > maximumContextLines || in.BeforeContext > maximumContextLines || in.AfterContext > maximumContextLines {
		return GrepResponse{}, fmt.Errorf("%w: grep result and context limits are outside their supported range", ErrInvalidInput)
	}
	if in.Pattern == "" {
		return GrepResponse{}, ErrEmptyPattern
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
	maxResults := in.MaxResults
	if maxResults == 0 {
		maxResults = defaultGrepMaxResults
	} else if maxResults > maximumSearchResults {
		return GrepResponse{}, fmt.Errorf("fs.LocalExecutor.Grep: max_results exceeds %d", maximumSearchResults)
	}
	response, err := l.grepFiles(ctx, root, base, info, executable, in, newRipgrepDecoder(mode, maxResults))
	if err != nil {
		return GrepResponse{}, fmt.Errorf("fs.LocalExecutor.Grep: %w", err)
	}
	return response, nil
}

func (l *LocalExecutor) Glob(ctx context.Context, in GlobRequest) (_ GlobResponse, err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return GlobResponse{}, contextErr
	}
	if in.MaxResults < 0 {
		return GlobResponse{}, fmt.Errorf("%w: max_results must not be negative", ErrInvalidInput)
	}
	if in.Pattern == "" {
		return GlobResponse{}, ErrEmptyPattern
	}
	if validationErr := validateGlobPattern(in.Pattern); validationErr != nil {
		return GlobResponse{}, validationErr
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

	maxResults := in.MaxResults
	if maxResults == 0 {
		maxResults = defaultGlobMaxResults
	} else if maxResults > maximumSearchResults {
		return GlobResponse{}, fmt.Errorf("fs.LocalExecutor.Glob: max_results exceeds %d", maximumSearchResults)
	}
	options := []doublestar.GlobOption{
		doublestar.WithFilesOnly(),
		doublestar.WithNoFollow(),
		doublestar.WithFailOnIOErrors(),
	}
	if in.IgnoreCase {
		options = append(options, doublestar.WithCaseInsensitive())
	}

	var (
		paths     []string
		truncated bool
	)
	pattern := slashpath.Join(filepath.ToSlash(base), filepath.ToSlash(in.Pattern))
	err = doublestar.GlobWalk(globFilesystem{FS: root.FS(), ctx: ctx}, pattern, func(name string, _ fs.DirEntry) error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		name = filepath.FromSlash(name)
		index, exists := slices.BinarySearch(paths, name)
		if exists {
			return nil
		}
		if len(paths) < maxResults {
			paths = slices.Insert(paths, index, name)
			return nil
		}
		truncated = true
		if index < maxResults {
			paths = slices.Insert(paths, index, name)
			paths = paths[:maxResults]
		}
		return nil
	}, options...)
	err = errors.Join(err, ctx.Err())
	if err != nil {
		return GlobResponse{}, fmt.Errorf("fs.LocalExecutor.Glob: %w", err)
	}
	return GlobResponse{Paths: paths, Truncated: truncated}, nil
}

func (l *LocalExecutor) ApplyPatch(ctx context.Context, in ApplyPatchRequest) (_ ApplyPatchResponse, err error) {
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
	targets := make(map[string]*mutationTarget)
	defer func() {
		for _, target := range targets {
			err = errors.Join(err, target.parent.Close())
		}
	}()
	for _, file := range resolved.files {
		for _, path := range file.touches() {
			target, openErr := openMutationTarget(root, path, path == file.newPath)
			if openErr != nil {
				return ApplyPatchResponse{}, openErr
			}
			for _, previous := range targets {
				if target.overlaps(previous) {
					return ApplyPatchResponse{}, errors.Join(fmt.Errorf("fs.ApplyPatch: duplicate target %s and %s", previous.path, path), target.parent.Close())
				}
			}
			targets[path] = target
		}
	}

	prepared := make([]preparedPatch, len(resolved.files))
	for i, file := range resolved.files {
		next, err := l.preparePatch(ctx, targets, file)
		if err != nil {
			return ApplyPatchResponse{}, err
		}
		prepared[i] = next
	}

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

func (l *LocalExecutor) preparePatch(
	ctx context.Context,
	targets map[string]*mutationTarget,
	file filePatch,
) (preparedPatch, error) {
	// A patch may not land on a file it did not open. Create says so by having no
	// origin; a move has one, but its destination is a new file all the same.
	if file.created() || file.moved() {
		if _, err := targets[file.newPath].parent.Stat(targets[file.newPath].name); err == nil {
			return preparedPatch{}, fmt.Errorf("fs.ApplyPatch: %s: file already exists", file.newPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return preparedPatch{}, fmt.Errorf("fs.ApplyPatch: %s: %w", file.newPath, err)
		}
	}

	var mode *os.FileMode
	var source []byte
	hadBOM, hadCRLF := false, false
	if !file.created() {
		info, err := targets[file.oldPath].parent.Stat(targets[file.oldPath].name)
		if err != nil {
			return preparedPatch{}, err
		}
		mode = new(info.Mode().Perm())
		data, err := readBoundedRootFile(ctx, targets[file.oldPath].parent, targets[file.oldPath].name, defaultMutationInputBytes)
		if err != nil {
			return preparedPatch{}, err
		}
		if looksBinary(data) {
			return preparedPatch{}, ErrBinaryFile
		}
		text, bom, crlf := normalizeText(data)
		hadBOM, hadCRLF = bom, crlf
		source = []byte(text)
	}

	patched, err := file.apply(source)
	if err != nil {
		return preparedPatch{}, err
	}
	if file.deleted() {
		if len(patched) != 0 {
			return preparedPatch{}, fmt.Errorf("fs.ApplyPatch: delete %s: patched content is not empty", file.path())
		}
		return preparedPatch{
			source: targets[file.oldPath],
			result: PatchFileResponse{Path: file.path(), Hunks: file.hunks(), Deleted: true},
		}, nil
	}

	result := PatchFileResponse{
		Path:    file.path(),
		Hunks:   file.hunks(),
		Created: file.created(),
	}
	prepared := preparedPatch{
		target: targets[file.newPath],
		data:   restoreFormat(string(patched), hadBOM, hadCRLF),
		mode:   mode,
	}
	if file.moved() {
		// The origin is reported, not just the destination: "moved" without saying
		// from where leaves the model to infer which file stopped existing.
		prepared.source = targets[file.oldPath]
		result.MovedFrom = file.oldPath
	}
	prepared.result = result
	return prepared, nil
}

func (l *LocalExecutor) runRipgrep(
	ctx context.Context,
	path string,
	args []string,
	input io.Reader,
	decoder *ripgrepDecoder,
) (GrepResponse, error) {
	command := exec.CommandContext(ctx, path, args...)
	command.Stdin = input
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return GrepResponse{}, fmt.Errorf("open ripgrep output: %w", err)
	}
	if err := command.Start(); err != nil {
		return GrepResponse{}, fmt.Errorf("start ripgrep: %w", err)
	}
	response, decodeErr := decoder.decode(stdout)
	if decodeErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return GrepResponse{}, decodeErr
	}
	waitErr := command.Wait()
	if waitErr == nil {
		return response, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return GrepResponse{}, ctxErr
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok && exitErr.ExitCode() == ripgrepNoMatchesExitCode {
		return response, nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		return GrepResponse{}, waitErr
	}
	return GrepResponse{}, fmt.Errorf("%w: %s", waitErr, message)
}

func (l *LocalExecutor) grepFiles(ctx context.Context, root *os.Root, base string, info os.FileInfo, executable string, in GrepInput, decoder *ripgrepDecoder) (GrepResponse, error) {
	if in.Glob != "" {
		if err := validateGlobPattern(in.Glob); err != nil {
			return GrepResponse{}, err
		}
	}
	var types []string
	if in.FileType != "" {
		data, err := exec.CommandContext(ctx, executable, "--no-config", "--type-list").Output()
		if err != nil {
			return GrepResponse{}, fmt.Errorf("read ripgrep file types: %w", err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if patterns, found := strings.CutPrefix(line, in.FileType+": "); found {
				types = strings.Split(patterns, ", ")
				break
			}
		}
		if len(types) == 0 {
			return GrepResponse{}, fmt.Errorf("fs.Grep: unknown file type %q", in.FileType)
		}
	}
	args := in.ripgrepArguments(decoder.mode)
	// Validate the search even if file selection is empty.
	if _, err := l.runRipgrep(ctx, executable, args, strings.NewReader(""), newRipgrepDecoder(decoder.mode, 1)); err != nil {
		return GrepResponse{}, err
	}
	search := func(path string) (err error) {
		name := filepath.ToSlash(path)
		if in.Glob != "" {
			candidate := name
			if !strings.Contains(in.Glob, "/") {
				candidate = filepath.Base(path)
			}
			matched, matchErr := doublestar.Match(in.Glob, candidate)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}
		if len(types) > 0 {
			matched := false
			for _, pattern := range types {
				if ok, _ := doublestar.Match(pattern, filepath.Base(path)); ok {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}
		file, _, err := openRegularRootFile(ctx, root, path)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		decoder.sourcePath = name
		_, err = l.runRipgrep(ctx, executable, args, file, decoder)
		return err
	}
	if !info.IsDir() {
		err := search(base)
		return decoder.response, err
	}
	err := fs.WalkDir(globFilesystem{FS: root.FS(), ctx: ctx}, filepath.ToSlash(base), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != filepath.ToSlash(base) && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return search(filepath.FromSlash(path))
	})
	return decoder.response, err
}
