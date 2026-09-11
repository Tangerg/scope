package skills

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/samber/lo"
)

// SkillFile is the required metadata file at the root of every skill
// directory.
const SkillFile = "SKILL.md"

// Repository is a read-only Agent Skills repository backed by an [fs.FS].
// Reads are lazy and per-call, so changes to the backing filesystem are
// visible without a refresh operation.
type Repository struct {
	fsys   fs.FS
	limits repositoryLimits
}

var _ Source = (*Repository)(nil)
var _ ResourceSource = (*Repository)(nil)

// NewRepository takes an [fs.FS] rather than a path so a skill set can come
// from an embedded bundle, an archive, or a test fixture without a temporary
// directory. Limits are resolved here because an unbounded repository would let
// a malformed skill exhaust memory during discovery, before any skill runs.
func NewRepository(fsys fs.FS, config RepositoryConfig) (*Repository, error) {
	if lo.IsNil(fsys) {
		return nil, ErrNilFilesystem
	}
	limits, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return &Repository{fsys: fsys, limits: limits}, nil
}

// NewDirectoryRepository roots the filesystem at a directory so a skill cannot
// escape it through a relative path, which matters because skill names reach
// this layer from untrusted bundles.
func NewDirectoryRepository(root string, config RepositoryConfig) (*Repository, error) {
	return NewRepository(rootedFS(root), config)
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

// List returns a summary for every valid skill directory, sorted by name.
// Invalid skill entries are skipped. Repository access failures are returned.
// A missing root directory is treated as an empty repository.
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
		if errors.Is(err, fs.ErrNotExist) {
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
		entries, readErr := reader.ReadDir(64)
		if err := contextError(ctx, "list"); err != nil {
			return nil, errors.Join(readErr, err)
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
		if errors.Is(readErr, io.EOF) {
			return summaries, nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("skills: list directory: %w", readErr)
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

// Load reads, parses, and validates one skill by directory name.
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
	if skill.Name != name {
		return nil, invalidSkill(name, fmt.Errorf(
			"%w: frontmatter %q vs directory %q",
			ErrNameMismatch,
			skill.Name,
			name,
		))
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
	frontmatter, readErr := readFrontmatter(ctx, file, r.limits.maxFrontmatterBytes)
	closeErr := file.Close()
	if closeErr != nil {
		return Summary{}, fmt.Errorf("skills: %s: %w", operation, errors.Join(readErr, closeErr))
	}
	if readErr != nil {
		if errors.Is(readErr, ErrNoFrontmatter) || errors.Is(readErr, ErrContentTooLarge) {
			return Summary{}, invalidSkill(name, readErr)
		}
		return Summary{}, fmt.Errorf("skills: %s: %w", operation, readErr)
	}
	skill, err := Parse(frontmatter)
	if ctxErr := contextError(ctx, operation); ctxErr != nil {
		return Summary{}, errors.Join(err, ctxErr)
	}
	if err != nil {
		return Summary{}, invalidSkill(name, err)
	}
	if skill.Name != name {
		return Summary{}, invalidSkill(name, fmt.Errorf(
			"%w: frontmatter %q vs directory %q", ErrNameMismatch, skill.Name, name,
		))
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
	if err == nil {
		return file, nil
	}
	if ctxErr := contextError(ctx, "open skill"); ctxErr != nil {
		return nil, errors.Join(err, ctxErr)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// A present bundle owns its name even without a metadata file. Only an
	// absent directory permits a lower-precedence source to supply the skill.
	entry, openErr := r.fsys.Open(name)
	if openErr != nil {
		if ctxErr := contextError(ctx, "open skill directory"); ctxErr != nil {
			return nil, errors.Join(err, openErr, ctxErr)
		}
		if errors.Is(openErr, fs.ErrNotExist) {
			return nil, errors.Join(ErrSkillNotFound, err, openErr)
		}
		return nil, errors.Join(err, openErr)
	}
	if closeErr := errors.Join(entry.Close(), contextError(ctx, "close skill directory")); closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	return nil, invalidSkill(name, err)
}

func readFrontmatter(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(contextReader{ctx: ctx, reader: reader}, maxBytes+1)
	buffered := bufio.NewReaderSize(limited, int(min(maxBytes+1, 4096)))

	var document bytes.Buffer
	lineNumber := 0
	for {
		text, readErr := buffered.ReadString('\n')
		if contextErr := contextError(ctx, "read frontmatter"); contextErr != nil {
			return nil, errors.Join(readErr, contextErr)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, errors.Join(readErr, ctx.Err())
		}
		line := strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
		if lineNumber == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
			if line != frontmatterFence {
				return nil, ErrNoFrontmatter
			}
		}
		if int64(document.Len()+len(line)+1) > maxBytes {
			return nil, fmt.Errorf("%w: frontmatter exceeds %d bytes", ErrContentTooLarge, maxBytes)
		}
		document.WriteString(line)
		document.WriteByte('\n')
		if lineNumber > 0 && line == frontmatterFence {
			return document.Bytes(), nil
		}
		lineNumber++
		if readErr != nil {
			return nil, ErrNoFrontmatter
		}
	}
}

// OpenResource opens a file bundled under a skill. The resource path is
// resolved relative to the skill directory. Lexical traversal is rejected;
// repositories returned by [NewDirectoryRepository] also reject symlink escapes.
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

	var file fs.File
	var err error
	if confined, ok := r.fsys.(confinedResourceFS); ok {
		file, err = confined.openInDir(name, resource)
	} else {
		file, err = r.fsys.Open(name + "/" + resource)
	}
	if err != nil {
		err = fmt.Errorf("skills: open resource %q/%q: %w", name, resource, err)
	}
	return checkedResourceFile(ctx, operation, name, resource, file, err)
}

func invalidSkill(name string, cause error) error {
	return fmt.Errorf("%w %q: %w", ErrInvalidSkill, name, cause)
}
