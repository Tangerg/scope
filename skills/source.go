package skills

import (
	"context"
	"errors"
	"fmt"
)

// Source is the read-only repository that discovers and loads skills. Its
// operations preserve progressive disclosure, so a consumer
// pulls in only as much as a task needs:
//
//   - List — name + description for every skill (level 1)
//   - Lookup — name + description for one exact skill (level 1)
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
	// Lookup checks one name's ownership and metadata without loading its
	// instructions or resources. An absent bundle returns ErrSkillNotFound;
	// invalid metadata returns ErrInvalidSkill. Access and cancellation errors
	// remain distinct from both. A valid summary does not guarantee that the
	// full document fits Load's limits or passes its validation.
	Lookup(ctx context.Context, name string) (Summary, error)
	// Load validates and returns one complete skill by exact name. The caller owns
	// the returned value. An absent skill returns ErrSkillNotFound; malformed
	// bundles, I/O errors, and cancellation must not be classified as absence.
	Load(ctx context.Context, name string) (*Skill, error)
}

// ResourceSource extends [Source] with progressive-disclosure level 3:
// opening a resource bundled under a skill directory.
type ResourceSource interface {
	Source
	ResourceOpener
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
