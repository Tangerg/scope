package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
)

// Source is the read-only repository that lists and loads skills. Its two
// operations mirror the first progressive-disclosure levels, so a consumer
// pulls in only as much as a task needs:
//
//   - List — name + description for every skill (level 1)
//   - Load — one skill's full instructions (level 2)
//
// Implementations must return valid Summary and Skill models and honor ctx
// cancellation. Cancellation errors preserve both context.Canceled or
// context.DeadlineExceeded and any custom cancellation cause through errors.Is.
// Concurrent I/O and cleanup failures remain identifiable through errors.Is.
type Source interface {
	// List returns detached, valid summaries in the implementation's stable
	// discovery order. Invalid skill bundles may be skipped, but repository I/O,
	// permission, and context failures must be returned rather than disguised as
	// an empty source.
	List(ctx context.Context) ([]Summary, error)
	// Load validates and returns one complete skill by exact name. The caller owns
	// the returned value. An absent skill returns ErrSkillNotFound; malformed
	// bundles, I/O errors, and cancellation must not be classified as absence.
	Load(ctx context.Context, name string) (*Skill, error)
}

// ResourceSource extends [Source] with progressive-disclosure level 3:
// opening a resource bundled under a skill directory.
type ResourceSource interface {
	Source
	// OpenResource opens one bundled resource beneath the exact skill root. It
	// must reject absolute paths, traversal, and symlink escape according to the
	// source's trust boundary and return a non-nil regular file on success.
	// The source validates the owning skill in this same operation. An absent
	// skill returns ErrSkillNotFound; a missing resource in an existing skill
	// returns fs.ErrNotExist without ErrSkillNotFound. Failed opens close any
	// acquired file; successful callers own and close the returned file.
	OpenResource(ctx context.Context, name, resource string) (fs.File, error)
}

func contextError(ctx context.Context, operation string) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, err) {
		err = cause
	} else {
		err = errors.Join(err, cause)
	}
	return fmt.Errorf("skills: %s: %w", operation, err)
}
