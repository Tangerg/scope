package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

const nullPatchPath = "/dev/null"

type unifiedPatch struct {
	files []filePatch
}

// duplicatePath reports a path two file patches both touch. Endpoints count, not
// just destinations: patching a file and moving another one onto it are two edits
// to one path, and applying both would make the result depend on their order.
func (u unifiedPatch) duplicatePath() string {
	seen := make(map[string]struct{}, len(u.files))
	for _, file := range u.files {
		for _, path := range file.touches() {
			if _, ok := seen[path]; ok {
				return path
			}
			seen[path] = struct{}{}
		}
	}
	return ""
}

// filePatch is Scope's execution view of an upstream parsed Git/unified diff.
// It owns filesystem endpoints while gitdiff owns syntax and hunk semantics.
type filePatch struct {
	parsed  *gitdiff.File
	oldPath string
	newPath string
}

func newFilePatch(parsed *gitdiff.File) filePatch {
	file := filePatch{
		parsed:  parsed,
		oldPath: cleanPatchPath(parsed.OldName),
		newPath: cleanPatchPath(parsed.NewName),
	}
	if parsed.IsNew || parsed.OldName == nullPatchPath {
		file.oldPath = ""
	}
	if parsed.IsDelete || parsed.NewName == nullPatchPath {
		file.newPath = ""
	}
	return file
}

func (f filePatch) path() string {
	if f.newPath != "" {
		return f.newPath
	}
	return f.oldPath
}

func (f filePatch) created() bool { return f.oldPath == "" && f.newPath != "" }
func (f filePatch) deleted() bool { return f.oldPath != "" && f.newPath == "" }

// moved reports the fourth shape: both headers name a real file and they differ,
// so the content is read at oldPath, patched, and lands at newPath while oldPath
// goes away. It is the one shape whose two endpoints are different files.
func (f filePatch) moved() bool {
	return f.oldPath != "" && f.newPath != "" && f.oldPath != f.newPath
}

// touches is every path this file patch reads, writes or removes.
func (f filePatch) touches() []string {
	if f.moved() {
		return []string{f.oldPath, f.newPath}
	}
	return []string{f.path()}
}

func (f filePatch) hunks() int {
	if f.parsed == nil {
		return 0
	}
	return len(f.parsed.TextFragments)
}

func (f filePatch) validate() error {
	if f.parsed == nil {
		return errors.New("fs.ApplyPatch: parsed file patch is nil")
	}
	if f.parsed.IsBinary {
		return fmt.Errorf("fs.ApplyPatch: %s: binary patches are not supported", f.path())
	}
	if f.parsed.IsCopy {
		return fmt.Errorf("fs.ApplyPatch: %s: copy patches are not supported", f.path())
	}
	if f.oldPath == "" && f.newPath == "" {
		return errors.New("fs.ApplyPatch: file patch is missing source and destination paths")
	}
	// A pure rename is the one patch with nothing to apply. Every other shape
	// without a hunk says nothing at all and is rejected.
	if f.hunks() == 0 && !f.moved() {
		return errors.New("fs.ApplyPatch: file patch has no hunks")
	}
	for _, fragment := range f.parsed.TextFragments {
		if fragment.OldPosition < 0 || fragment.NewPosition < 0 {
			return fmt.Errorf("fs.ApplyPatch: %s: hunk positions must not be negative", f.path())
		}
	}
	if f.oldPath != "" {
		if err := validatePatchPath(f.oldPath); err != nil {
			return err
		}
	}
	if f.newPath != "" {
		if err := validatePatchPath(f.newPath); err != nil {
			return err
		}
	}
	return nil
}

func (f filePatch) apply(source []byte) ([]byte, error) {
	var output bytes.Buffer
	if err := gitdiff.Apply(&output, bytes.NewReader(source), f.parsed); err != nil {
		return nil, fmt.Errorf("fs.ApplyPatch: hunk for %s does not match: %w", f.path(), err)
	}
	return output.Bytes(), nil
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

	var locks []string
	for _, file := range resolved.files {
		locks = append(locks, file.touches()...)
	}

	// Both endpoints of a move are locked: it removes one file and creates
	// another, and holding only the destination would let a concurrent write to
	// the origin land in a file this call is about to delete.
	slices.Sort(locks)
	for _, path := range locks {
		unlock := l.lockPath(path)
		defer unlock()
	}

	prepared := make([]preparedPatch, len(resolved.files))
	for i, file := range resolved.files {
		next, err := l.preparePatch(ctx, root, file)
		if err != nil {
			return ApplyPatchResponse{}, err
		}
		prepared[i] = next
	}

	var out ApplyPatchResponse
	for _, file := range prepared {
		if err := file.commit(root); err != nil {
			return ApplyPatchResponse{}, err
		}
		out.Files = append(out.Files, file.result)
		out.Hunks += file.result.Hunks
	}
	return out, nil
}

func validatePatchPath(path string) error {
	if path == "" || path == "." || path == string(filepath.Separator) {
		return fmt.Errorf("fs.ApplyPatch: invalid file path %q", path)
	}
	return nil
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
	if path := resolved.duplicatePath(); path != "" {
		return unifiedPatch{}, fmt.Errorf("fs.ApplyPatch: duplicate file patch for %s", path)
	}
	return resolved, nil
}

// preparedPatch is one file patch's committed outcome, computed before anything
// is written so a patch that cannot apply changes nothing.
type preparedPatch struct {
	// path is where the content lands, empty for a delete.
	path string
	// source is the file to remove once the content has landed: a delete's own
	// path, or the origin of a move. Empty when nothing is removed.
	source string
	data   []byte
	mode   os.FileMode
	result PatchFileResponse
}

// commit writes before it removes, so a failure between the two leaves the
// content somewhere rather than nowhere.
func (p preparedPatch) commit(root *os.Root) error {
	if p.path != "" {
		if err := atomicWriteRootFile(root, p.path, p.data, p.mode); err != nil {
			return err
		}
	}
	if p.source != "" && p.source != p.path {
		return root.Remove(p.source)
	}
	return nil
}

func (l *LocalExecutor) preparePatch(
	ctx context.Context,
	root *os.Root,
	file filePatch,
) (preparedPatch, error) {
	// A patch may not land on a file it did not open. Create says so by having no
	// origin; a move has one, but its destination is a new file all the same.
	if file.created() || file.moved() {
		if _, err := root.Stat(file.newPath); err == nil {
			return preparedPatch{}, fmt.Errorf("fs.ApplyPatch: %s: file already exists", file.newPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return preparedPatch{}, fmt.Errorf("fs.ApplyPatch: %s: %w", file.newPath, err)
		}
	}

	mode := defaultFileMode
	var source []byte
	hadBOM, hadCRLF := false, false
	if !file.created() {
		info, err := root.Stat(file.oldPath)
		if err != nil {
			return preparedPatch{}, err
		}
		mode = info.Mode().Perm()
		data, err := readBoundedRootFile(ctx, root, file.oldPath, defaultMutationInputBytes)
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
			source: file.oldPath,
			result: PatchFileResponse{Path: file.path(), Hunks: file.hunks(), Deleted: true},
		}, nil
	}

	result := PatchFileResponse{
		Path:    file.path(),
		Hunks:   file.hunks(),
		Created: file.created(),
	}
	prepared := preparedPatch{
		path: file.newPath,
		data: restoreFormat(string(patched), hadBOM, hadCRLF),
		mode: mode,
	}
	if file.moved() {
		// The origin is reported, not just the destination: "moved" without saying
		// from where leaves the model to infer which file stopped existing.
		prepared.source = file.oldPath
		result.MovedFrom = file.oldPath
	}
	prepared.result = result
	return prepared, nil
}

func parseUnifiedPatch(patch string) (unifiedPatch, error) {
	if strings.TrimSpace(patch) == "" {
		return unifiedPatch{}, errors.New("fs.ApplyPatch: patch must not be empty")
	}
	normalized := strings.ReplaceAll(patch, "\r\n", "\n")
	files, _, err := gitdiff.Parse(strings.NewReader(normalized))
	if err != nil {
		return unifiedPatch{}, fmt.Errorf("fs.ApplyPatch: parse unified diff: %w", err)
	}
	if len(files) == 0 {
		return unifiedPatch{}, errors.New("fs.ApplyPatch: no file patches found")
	}
	parsed := unifiedPatch{files: make([]filePatch, len(files))}
	for index, file := range files {
		parsed.files[index] = newFilePatch(file)
	}
	return parsed, nil
}

func cleanPatchPath(path string) string {
	if path == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(path, "a/"); ok {
		path = rest
	} else if rest, ok := strings.CutPrefix(path, "b/"); ok {
		path = rest
	}
	return filepath.Clean(path)
}
