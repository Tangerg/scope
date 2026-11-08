package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/samber/lo"
)

const (
	SkillFile              = "SKILL.md"
	directoryReadBatchSize = 64
)

// ResourceFS supplies repository discovery and directory-confined resource
// access. An ordinary fs.FS does not promise confinement: fs.Sub and os.DirFS
// may follow symlinks outside their apparent roots.
type ResourceFS interface {
	fs.FS
	// OpenInDir opens name relative to dir in this filesystem. Both paths use
	// io/fs syntax. Resolution must remain within dir, including through symlinks
	// and concurrent directory changes; lexical prefix checks are insufficient.
	// The returned file is independently owned by the caller. Errors retain
	// their filesystem causes, and a failed open closes any acquired handles.
	OpenInDir(dir, name string) (fs.File, error)
}

// Repository is a read-only Agent Skills repository backed by a [ResourceFS].
// Reads are lazy and per-call, so changes to the backing filesystem are
// visible without a refresh operation.
type Repository struct {
	fsys   ResourceFS
	limits repositoryLimits
}

var _ Source = (*Repository)(nil)
var _ ResourceSource = (*Repository)(nil)

// NewRepository requires the backend to own directory confinement rather than
// assuming an arbitrary fs.FS rejects symlink escape. Embedded and archive
// backends must provide the same capability; real directories use
// [NewDirectoryRepository]. Limits bound discovery before any skill runs.
func NewRepository(fsys ResourceFS, config RepositoryConfig) (*Repository, error) {
	if lo.IsNil(fsys) {
		return nil, ErrNilFilesystem
	}
	limits, err := config.normalize()
	if err != nil {
		return nil, err
	}
	return &Repository{fsys: fsys, limits: limits}, nil
}

// NewDirectoryRepository borrows the Host's open directory. Renaming or
// replacing its path cannot switch the repository to another tree. The Host
// owns root: closing it prevents future opens but does not invalidate files
// that callers have already acquired. It returns an error matching
// [errors.ErrUnsupported] on js and plan9, where os.Root cannot preserve this
// authority across filesystem changes.
func NewDirectoryRepository(root *os.Root, config RepositoryConfig) (*Repository, error) {
	if root == nil {
		return nil, ErrNilFilesystem
	}
	switch runtime.GOOS {
	case "js", "plan9":
		return nil, fmt.Errorf("skills: directory confinement on %s: %w", runtime.GOOS, errors.ErrUnsupported)
	}
	if _, err := root.Stat("."); err != nil {
		return nil, err
	}
	return NewRepository(rootedFS{root: root}, config)
}

func (r *Repository) validate() error {
	if r == nil || lo.IsNil(r.fsys) {
		return ErrNilFilesystem
	}
	if r.limits.maxEntries <= 0 || r.limits.maxFrontmatterBytes <= 0 || r.limits.maxSkillBytes <= 0 {
		return ErrInvalidLimit
	}
	return nil
}

// List returns summaries sorted by name, skipping invalid skill entries. A
// missing root directory is an empty repository.
func (r *Repository) List(ctx context.Context) (summaries []Summary, err error) {
	if validationErr := r.validate(); validationErr != nil {
		return nil, validationErr
	}
	if contextErr := contextError(ctx, "list"); contextErr != nil {
		return nil, contextErr
	}
	directory, err := r.fsys.Open(".")
	if err != nil {
		if contextErr := contextError(ctx, "list"); contextErr != nil {
			return nil, errors.Join(err, contextErr)
		}
		if onlyMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: list: %w", err)
	}
	defer func() {
		if closeErr := directory.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("skills: close repository directory: %w", closeErr))
		}
		err = errors.Join(err, contextError(ctx, "list"))
	}()
	if contextErr := contextError(ctx, "list"); contextErr != nil {
		return nil, contextErr
	}
	reader, ok := directory.(fs.ReadDirFile)
	if !ok {
		return nil, errors.New("skills: list: filesystem directory does not implement fs.ReadDirFile")
	}

	summaries, err = r.readSummaries(ctx, reader)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(summaries, func(a, b Summary) int {
		return strings.Compare(a.Name, b.Name)
	})
	return summaries, nil
}

func (r *Repository) readSummaries(ctx context.Context, reader fs.ReadDirFile) ([]Summary, error) {
	summaries := make([]Summary, 0)
	entriesSeen := 0
	for {
		if err := contextError(ctx, "list"); err != nil {
			return nil, err
		}
		entries, readErr := reader.ReadDir(directoryReadBatchSize)
		if err := contextError(ctx, "list"); err != nil {
			return nil, errors.Join(readErr, err)
		}
		if readErr != nil && readErr != io.EOF { //nolint:errorlint // A joined EOF can include an I/O failure; only the bare sentinel ends discovery.
			return nil, fmt.Errorf("skills: list directory: %w", readErr)
		}
		for _, entry := range entries {
			entriesSeen++
			if entriesSeen > r.limits.maxEntries {
				return nil, fmt.Errorf("%w: limit %d", ErrRepositoryLarge, r.limits.maxEntries)
			}
			summary, include, err := r.summaryForEntry(ctx, entry)
			if err != nil {
				return nil, err
			}
			if include {
				summaries = append(summaries, summary)
			}
		}
		if readErr != nil {
			return summaries, nil
		}
	}
}

func (r *Repository) summaryForEntry(ctx context.Context, entry fs.DirEntry) (Summary, bool, error) {
	if err := contextError(ctx, "list"); err != nil {
		return Summary{}, false, err
	}
	if !entry.IsDir() || ValidateName(entry.Name()) != nil {
		return Summary{}, false, nil
	}
	summary, err := r.Lookup(ctx, entry.Name())
	if ctxErr := contextError(ctx, "list"); ctxErr != nil {
		return Summary{}, false, errors.Join(err, ctxErr)
	}
	if err == nil {
		return summary, true, nil
	}
	if errors.Is(err, ErrInvalidSkill) {
		return Summary{}, false, nil
	}
	return Summary{}, false, fmt.Errorf("skills: list: %w", err)
}

func (r *Repository) Load(ctx context.Context, name string) (*Skill, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("load %q", name)
	if err := contextError(ctx, operation); err != nil {
		return nil, err
	}
	data, err := r.readSkillFile(ctx, name, r.limits.maxSkillBytes)
	if err != nil {
		return nil, err
	}

	skill, err := Parse(data)
	if ctxErr := contextError(ctx, operation); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, invalidSkill(name, err)
	}
	if err := skill.matchDirectoryName(name); err != nil {
		return nil, invalidSkill(name, err)
	}
	return skill, nil
}

// Lookup reads only bounded frontmatter. The named directory owns the bundle
// even when SKILL.md is missing or its metadata is malformed.
func (r *Repository) Lookup(ctx context.Context, name string) (Summary, error) {
	if err := r.validate(); err != nil {
		return Summary{}, err
	}
	if err := ValidateName(name); err != nil {
		return Summary{}, err
	}
	operation := fmt.Sprintf("load summary %q", name)
	file, err := r.openSkillFile(ctx, name)
	if err != nil {
		return Summary{}, fmt.Errorf("skills: %s: %w", operation, err)
	}
	frontmatter, _, readErr := readFrontmatter(ctx, file, r.limits.maxFrontmatterBytes)
	closeErr := errors.Join(file.Close(), contextError(ctx, "close skill"))
	if closeErr != nil {
		return Summary{}, fmt.Errorf("skills: %s: %w", operation, errors.Join(readErr, closeErr))
	}
	if readErr != nil {
		if errors.Is(readErr, ErrNoFrontmatter) || errors.Is(readErr, ErrContentTooLarge) {
			return Summary{}, invalidSkill(name, readErr)
		}
		return Summary{}, fmt.Errorf("skills: %s: %w", operation, readErr)
	}
	skill, err := parseSkill(frontmatter, nil)
	if ctxErr := contextError(ctx, operation); ctxErr != nil {
		return Summary{}, errors.Join(err, ctxErr)
	}
	if err != nil {
		return Summary{}, invalidSkill(name, err)
	}
	if err := skill.matchDirectoryName(name); err != nil {
		return Summary{}, invalidSkill(name, err)
	}
	return skill.Summary(), nil
}

func (r *Repository) readSkillFile(ctx context.Context, name string, maxBytes int64) ([]byte, error) {
	file, err := r.openSkillFile(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("skills: load %q: %w", name, err)
	}
	data, truncated, readErr := readBounded(ctx, file, maxBytes)
	closeErr := file.Close()
	if readErr = errors.Join(readErr, closeErr, contextError(ctx, "read skill")); readErr != nil {
		return nil, fmt.Errorf("skills: load %q: %w", name, readErr)
	}
	if truncated {
		return nil, fmt.Errorf("%w: skill %q exceeds %d bytes", ErrContentTooLarge, name, maxBytes)
	}
	return data, nil
}

func (r *Repository) openSkillFile(ctx context.Context, name string) (fs.File, error) {
	if err := contextError(ctx, "open skill"); err != nil {
		return nil, err
	}
	file, err := r.fsys.Open(name + "/" + SkillFile)
	if err != nil {
		return nil, r.missingSkillFileError(ctx, name, err)
	}
	info, statErr := file.Stat()
	if statErr = errors.Join(statErr, contextError(ctx, "open skill")); statErr != nil {
		return nil, errors.Join(statErr, file.Close(), contextError(ctx, "close skill"))
	}
	if !info.Mode().IsRegular() {
		cause := fmt.Errorf("%s must be a regular file: mode %s", SkillFile, info.Mode().Type())
		// List may skip invalid skills, but must propagate cleanup failures.
		if closeErr := errors.Join(file.Close(), contextError(ctx, "close skill")); closeErr != nil {
			return nil, errors.Join(cause, closeErr)
		}
		return nil, invalidSkill(name, cause)
	}
	return file, nil
}

// missingSkillFileError classifies a failed SKILL.md open. A present bundle
// owns its name even without a metadata file; only an absent directory permits
// a lower-precedence source to supply the skill.
func (r *Repository) missingSkillFileError(ctx context.Context, name string, err error) error {
	if ctxErr := contextError(ctx, "open skill"); ctxErr != nil {
		return errors.Join(err, ctxErr)
	}
	if !onlyMissing(err) {
		return err
	}
	entry, openErr := r.fsys.Open(name)
	if openErr != nil {
		if ctxErr := contextError(ctx, "open skill directory"); ctxErr != nil {
			return errors.Join(err, openErr, ctxErr)
		}
		if onlyMissing(openErr) {
			return errors.Join(ErrSkillNotFound, err, openErr)
		}
		return errors.Join(err, openErr)
	}
	if closeErr := errors.Join(entry.Close(), contextError(ctx, "close skill directory")); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return invalidSkill(name, err)
}

// OpenResource validates the skill and path before asking its ResourceFS to
// open within that skill's directory. Repository owns regular-file validation;
// the filesystem owns resolution confinement.
func (r *Repository) OpenResource(ctx context.Context, name, resource string) (fs.File, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if err := validateResourcePath(resource); err != nil {
		return nil, err
	}
	if _, err := r.Load(ctx, name); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("open resource %q/%q", name, resource)
	if err := contextError(ctx, operation); err != nil {
		return nil, err
	}

	file, err := r.fsys.OpenInDir(name, resource)
	if err != nil {
		err = fmt.Errorf("skills: open resource %q/%q: %w", name, resource, err)
	}
	return checkedResourceFile(ctx, operation, name, resource, file, err)
}

func invalidSkill(name string, cause error) error {
	return fmt.Errorf("%w %q: %w", ErrInvalidSkill, name, cause)
}
