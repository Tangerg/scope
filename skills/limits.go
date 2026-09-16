package skills

import "fmt"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultMaxRepositoryEntries = 512
	DefaultMaxFrontmatterBytes  = int64(64 * 1024)
	DefaultMaxSkillBytes        = int64(1024 * 1024)
	DefaultMaxResourceBytes     = int64(1024 * 1024)
	maxBoundedReadBytes         = int64(^uint(0)>>1) - 1
)

// RepositoryConfig bounds repository discovery and skill-document reads.
// Zero fields select the package defaults.
type RepositoryConfig struct {
	MaxEntries          int
	MaxFrontmatterBytes int64
	MaxSkillBytes       int64
}

type repositoryLimits struct {
	maxEntries          int
	maxFrontmatterBytes int64
	maxSkillBytes       int64
}

func (r RepositoryConfig) normalize() (repositoryLimits, error) {
	if r.MaxEntries < 0 || r.MaxFrontmatterBytes < 0 || r.MaxSkillBytes < 0 {
		return repositoryLimits{}, fmt.Errorf("%w: repository limits must not be negative", ErrInvalidLimit)
	}
	if r.MaxFrontmatterBytes > maxBoundedReadBytes || r.MaxSkillBytes > maxBoundedReadBytes {
		return repositoryLimits{}, fmt.Errorf("%w: byte limits are too large", ErrInvalidLimit)
	}
	limits := repositoryLimits{
		maxEntries:          r.MaxEntries,
		maxFrontmatterBytes: r.MaxFrontmatterBytes,
		maxSkillBytes:       r.MaxSkillBytes,
	}
	if limits.maxEntries == 0 {
		limits.maxEntries = DefaultMaxRepositoryEntries
	}
	if limits.maxFrontmatterBytes == 0 {
		limits.maxFrontmatterBytes = DefaultMaxFrontmatterBytes
	}
	if limits.maxSkillBytes == 0 {
		limits.maxSkillBytes = DefaultMaxSkillBytes
	}
	if limits.maxFrontmatterBytes > limits.maxSkillBytes {
		return repositoryLimits{}, fmt.Errorf("%w: frontmatter limit exceeds skill limit", ErrInvalidLimit)
	}
	return limits, nil
}
