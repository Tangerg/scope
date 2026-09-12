package fs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

const nullPatchPath = "/dev/null"

type unifiedPatch struct {
	files []filePatch
}

// validatePaths rejects write sets whose meaning depends on commit order.
// A file cannot also be an ancestor directory of another endpoint.
func (u unifiedPatch) validatePaths() error {
	seen := make(map[string]struct{}, len(u.files))
	for _, file := range u.files {
		for _, path := range file.touches() {
			if _, ok := seen[path]; ok {
				return fmt.Errorf("fs.ApplyPatch: duplicate file patch for %s", path)
			}
			seen[path] = struct{}{}
		}
	}
	for path := range seen {
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			if _, exists := seen[parent]; exists {
				return fmt.Errorf("fs.ApplyPatch: file %s is an ancestor of %s", parent, path)
			}
		}
	}
	return nil
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

func validatePatchPath(path string) error {
	if path == "" || path == "." || path == string(filepath.Separator) {
		return fmt.Errorf("fs.ApplyPatch: invalid file path %q", path)
	}
	return nil
}

// preparedPatch holds a validated file mutation. Preparation changes no files;
// commit reports only effects acknowledged by the filesystem.
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
func (p preparedPatch) commit(root *os.Root) (PatchFileResponse, error) {
	if p.path != "" {
		if err := atomicWriteRootFile(root, p.path, p.data, p.mode); err != nil {
			return PatchFileResponse{}, fmt.Errorf("fs.ApplyPatch: write %s: %w", p.path, err)
		}
	}
	if p.source != "" && p.source != p.path {
		if err := root.Remove(p.source); err != nil {
			var result PatchFileResponse
			if p.path != "" {
				// A move whose removal fails created its destination but did not
				// move the source. Report that actual effect rather than the plan.
				result = PatchFileResponse{Path: p.path, Hunks: p.result.Hunks, Created: true}
			}
			return result, fmt.Errorf("fs.ApplyPatch: remove %s: %w", p.source, err)
		}
	}
	return p.result, nil
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
