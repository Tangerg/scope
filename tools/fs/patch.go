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

func (f filePatch) moved() bool {
	return f.oldPath != "" && f.newPath != "" && f.oldPath != f.newPath
}

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
	if slices.ContainsFunc(f.parsed.TextFragments, func(fragment *gitdiff.TextFragment) bool {
		return fragment.OldPosition < 0 || fragment.NewPosition < 0
	}) {
		return fmt.Errorf("fs.ApplyPatch: %s: hunk positions must not be negative", f.path())
	}
	for _, path := range f.touches() {
		if path == "." || path == string(filepath.Separator) {
			return fmt.Errorf("fs.ApplyPatch: invalid file path %q", path)
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

func (f filePatch) prepare(ctx context.Context, targets patchTargets) (preparedPatch, error) {
	// A patch may not land on a file it did not open. Create says so by having no
	// origin; a move has one, but its destination is a new file all the same.
	if f.created() || f.moved() {
		if err := targets[f.newPath].requireAbsent(); err != nil {
			return preparedPatch{}, err
		}
	}
	var source sourceText
	var mode *os.FileMode
	if !f.created() {
		var err error
		if source, err = targets[f.oldPath].readText(ctx); err != nil {
			return preparedPatch{}, err
		}
		mode = &source.mode
	}
	patched, err := f.apply([]byte(source.text))
	if err != nil {
		return preparedPatch{}, err
	}
	if f.deleted() {
		if len(patched) != 0 {
			return preparedPatch{}, fmt.Errorf("fs.ApplyPatch: delete %s: patched content is not empty", f.path())
		}
		return preparedPatch{
			source: targets[f.oldPath],
			result: PatchFileResponse{Path: f.path(), Hunks: f.hunks(), Deleted: true},
		}, nil
	}
	prepared := preparedPatch{
		target: targets[f.newPath],
		data:   source.restore(string(patched)),
		mode:   mode,
		result: PatchFileResponse{Path: f.path(), Hunks: f.hunks(), Created: f.created()},
	}
	if f.moved() {
		prepared.source = targets[f.oldPath]
		prepared.result.MovedFrom = f.oldPath
	}
	return prepared, nil
}

// patchTargets pins every endpoint of a patch before preparation, keyed by its
// root-relative path, and rejects endpoints that name the same directory entry.
type patchTargets map[string]*mutationTarget

func (p patchTargets) open(root *os.Root, files []filePatch) error {
	for _, file := range files {
		for _, path := range file.touches() {
			if err := p.add(root, path, path == file.newPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p patchTargets) add(root *os.Root, path string, allowMissingParents bool) error {
	target, err := openMutationTarget(root, path, allowMissingParents)
	if err != nil {
		return err
	}
	for _, previous := range p {
		if target.overlaps(previous) {
			return errors.Join(fmt.Errorf("fs.ApplyPatch: duplicate target %s and %s", previous.path, path), target.parent.Close())
		}
	}
	p[path] = target
	return nil
}

func (p patchTargets) close() error {
	var err error
	for _, target := range p {
		err = errors.Join(err, target.parent.Close())
	}
	return err
}

// preparedPatch holds a validated file mutation. Commit reports only effects
// acknowledged by the filesystem.
type preparedPatch struct {
	target *mutationTarget
	source *mutationTarget
	data   []byte
	mode   *os.FileMode
	result PatchFileResponse
}

// A move publishes before removing its source, preserving partial outcomes.
func (p preparedPatch) commit(ctx context.Context) (PatchFileResponse, error) {
	if p.target != nil {
		if err := p.target.write(ctx, p.data, p.mode); err != nil {
			return PatchFileResponse{}, fmt.Errorf("fs.ApplyPatch: write %s: %w", p.target.path, err)
		}
	}
	if p.source != nil {
		if err := p.source.parent.Remove(p.source.name); err != nil {
			var result PatchFileResponse
			if p.target != nil {
				result = PatchFileResponse{Path: p.target.path, Hunks: p.result.Hunks, Created: true}
			}
			return result, fmt.Errorf("fs.ApplyPatch: remove %s: %w", p.source.path, err)
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
		return unifiedPatch{}, fmt.Errorf("fs.ApplyPatch: parse unified diff: %w. "+
			"In each @@ -oldStart,oldCount +newStart,newCount @@ header, oldCount must count context and removed lines, "+
			"and newCount must count context and added lines. Recount every hunk", err)
	}
	if len(files) == 0 {
		return unifiedPatch{}, errors.New("fs.ApplyPatch: no file patches found")
	}
	parsed := unifiedPatch{files: make([]filePatch, len(files))}
	for index, file := range files {
		next := newFilePatch(file)
		if err := next.validate(); err != nil {
			return unifiedPatch{}, err
		}
		parsed.files[index] = next
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
