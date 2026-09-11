package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/samber/lo"
)

// Merge layers several resource sources into one. Earlier sources take
// precedence: on a name collision the first source that has the skill wins, so
// callers express precedence by order (e.g. a project source before a global
// one). The winning source owns the complete skill bundle; missing resources
// do not fall through to a lower-precedence copy with the same name.
// Discovery resolves names through Load too: a malformed winning bundle is
// reported instead of advertising a lower-precedence copy as loadable.
//
// Nil and typed-nil sources are dropped. Merge of none yields an empty source
// (List returns nothing, Load reports not found).
func Merge(sources ...ResourceSource) ResourceSource {
	kept := make([]ResourceSource, 0, len(sources))
	for _, s := range sources {
		if !lo.IsNil(s) {
			kept = append(kept, s)
		}
	}
	return &merged{sources: kept}
}

type merged struct {
	sources []ResourceSource
}

var _ Source = (*merged)(nil)
var _ ResourceSource = (*merged)(nil)

// List discovers candidate names, then uses the same owner resolution as Load.
// It returns sorted summaries or an error from the winning bundle. Loading each
// candidate is necessary because Source.List may omit malformed bundles.
func (m *merged) List(ctx context.Context) ([]Summary, error) {
	if err := contextError(ctx, "list"); err != nil {
		return nil, err
	}
	var names []string
	seen := make(map[string]struct{})
	for _, src := range m.sources {
		if err := contextError(ctx, "list"); err != nil {
			return nil, err
		}
		summaries, err := src.List(ctx)
		if ctxErr := contextError(ctx, "list"); ctxErr != nil {
			return nil, errors.Join(err, ctxErr)
		}
		if err != nil {
			return nil, err
		}
		for _, summary := range summaries {
			if err := summary.Validate(); err != nil {
				return nil, fmt.Errorf("%w summary %q: %w", ErrInvalidSkill, summary.Name, err)
			}
			if _, dup := seen[summary.Name]; dup {
				continue
			}
			seen[summary.Name] = struct{}{}
			names = append(names, summary.Name)
		}
	}
	slices.Sort(names)
	var out []Summary
	for _, name := range names {
		skill, err := m.Load(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("skills: list: %w", err)
		}
		out = append(out, skill.Summary())
	}
	return out, nil
}

// Load returns the skill from the first source that has it. Missing skills are
// skipped; malformed skills return immediately so a broken higher-precedence
// copy is not silently masked by a lower one.
func (m *merged) Load(ctx context.Context, name string) (*Skill, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("load %q", name)
	return m.resolve(ctx, name, operation, func(src ResourceSource) (*Skill, error) {
		skill, err := src.Load(ctx, name)
		if ctxErr := contextError(ctx, operation); ctxErr != nil {
			return nil, errors.Join(err, ctxErr)
		}
		if err != nil {
			return nil, err
		}
		if err := skill.Validate(); err != nil {
			return nil, invalidSkill(name, err)
		}
		if skill.Name != name {
			return nil, invalidSkill(name, fmt.Errorf("%w: loaded %q vs requested %q", ErrNameMismatch, skill.Name, name))
		}
		return skill, nil
	})
}

// OpenResource opens a resource from the source that owns the winning skill.
// A lower-precedence copy must never contribute files to a higher-precedence
// skill with the same name.
func (m *merged) OpenResource(ctx context.Context, name, resource string) (fs.File, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if err := validateResourcePath(resource); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("open resource %q/%q", name, resource)
	return m.resolve(ctx, name, operation, func(src ResourceSource) (fs.File, error) {
		file, err := src.OpenResource(ctx, name, resource)
		return receivedResourceFile(ctx, operation, name, resource, file, err)
	})
}

// resolve only falls through when the skill itself is absent. A missing file
// never allows a lower-precedence source to supply part of the winning bundle.
func (m *merged) resolve[T any](ctx context.Context, name, operation string, lookup func(ResourceSource) (T, error)) (T, error) {
	var zero T
	if err := contextError(ctx, operation); err != nil {
		return zero, err
	}
	var errs []error
	for _, src := range m.sources {
		if err := contextError(ctx, operation); err != nil {
			return zero, err
		}
		value, err := lookup(src)
		if err == nil {
			return value, nil
		}
		if ctxErr := contextError(ctx, operation); ctxErr != nil {
			return zero, errors.Join(err, ctxErr)
		}
		if !errors.Is(err, ErrSkillNotFound) {
			return zero, err
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return zero, fmt.Errorf("skills: skill %q: %w", name, ErrSkillNotFound)
	}
	return zero, errors.Join(errs...)
}
