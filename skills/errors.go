package skills

import (
	"errors"
	"fmt"
	"io/fs"
)

var (
	ErrInvalidSkill = errors.New("skills: invalid skill")
	// ErrSkillNotFound identifies an absent skill, separately from a missing
	// resource in an existing skill. It also matches fs.ErrNotExist.
	ErrSkillNotFound      = fmt.Errorf("skills: skill not found: %w", fs.ErrNotExist)
	ErrNilSkill           = errors.New("skills: skill must not be nil")
	ErrNilFilesystem      = errors.New("skills: filesystem must not be nil")
	ErrNilSource          = errors.New("skills: source must not be nil")
	ErrNilResourceFile    = errors.New("skills: resource source returned a nil file without an error")
	ErrResourceNotRegular = errors.New("skills: resource must be a regular file")
	ErrInvalidLimit       = errors.New("skills: invalid limit")
	ErrContentTooLarge    = errors.New("skills: content exceeds configured limit")
	ErrRepositoryLarge    = errors.New("skills: repository exceeds configured entry limit")

	ErrNoFrontmatter = errors.New("skills: SKILL.md must open with a YAML frontmatter block delimited by ---")

	ErrNameEmpty    = errors.New("skills: name must not be empty")
	ErrNameTooLong  = errors.New("skills: name exceeds 64 characters")
	ErrNameInvalid  = errors.New("skills: name must be lowercase alphanumerics joined by single hyphens (no leading, trailing, or consecutive hyphens)")
	ErrNameMismatch = errors.New("skills: frontmatter name must match the skill directory name")

	ErrDescriptionEmpty   = errors.New("skills: description must not be empty")
	ErrDescriptionTooLong = errors.New("skills: description exceeds 1024 characters")

	ErrCompatibilityTooLong = errors.New("skills: compatibility exceeds 500 characters")

	ErrResourcePath = errors.New("skills: resource path escapes the skill directory")
)
