package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/samber/lo"
)

// Merge layers several resource sources into one. Earlier sources take
// precedence: on a name collision the first source that has the skill wins, so
// callers express precedence by order (e.g. a project source before a global
// one). The winning source owns the complete skill bundle; missing resources
// do not fall through to a lower-precedence copy with the same name.
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

// List unions every source's summaries, keeping the first occurrence of each
// name (precedence by source order) and sorting the result by name.
func (m *merged) List(ctx context.Context) ([]Summary, error) {
	if err := contextError(ctx, "list"); err != nil {
		return nil, err
	}
	var out []Summary
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
				continue // a higher-precedence source already provided this name
			}
			seen[summary.Name] = struct{}{}
			out = append(out, summary)
		}
	}
	slices.SortFunc(out, func(a, b Summary) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// Load returns the skill from the first source that has it. Missing skills are
// skipped; malformed skills return immediately so a broken higher-precedence
// copy is not silently masked by a lower one.
func (m *merged) Load(ctx context.Context, name string) (*Skill, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	bundle, err := m.resolve(ctx, name, fmt.Sprintf("load %q", name))
	if err != nil {
		return nil, err
	}
	return bundle.skill, nil
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
	bundle, err := m.resolve(ctx, name, operation)
	if err != nil {
		return nil, err
	}
	return bundle.openResource(ctx, resource, operation)
}

// skillBundle keeps the winning skill and its resource source inseparable.
// This prevents lower-precedence resources from leaking into a higher-
// precedence skill with the same name.
type skillBundle struct {
	source ResourceSource
	skill  *Skill
}

func (s *skillBundle) openResource(ctx context.Context, resource, operation string) (fs.File, error) {
	file, err := s.source.OpenResource(ctx, s.skill.Name, resource)
	return checkedResourceFile(ctx, operation, s.skill.Name, resource, file, err)
}

// resolve returns the first complete bundle that owns name. Only
// not-exist errors fall through; malformed higher-precedence skills remain
// authoritative rather than being silently shadowed by a lower source.
func (m *merged) resolve(ctx context.Context, name, operation string) (*skillBundle, error) {
	if err := contextError(ctx, operation); err != nil {
		return nil, err
	}
	var errs []error
	for _, src := range m.sources {
		if err := contextError(ctx, operation); err != nil {
			return nil, err
		}
		skill, err := src.Load(ctx, name)
		if ctxErr := contextError(ctx, operation); ctxErr != nil {
			return nil, errors.Join(err, ctxErr)
		}
		if err == nil {
			if validateErr := skill.Validate(); validateErr != nil {
				return nil, invalidSkill(name, validateErr)
			}
			if skill.Name != name {
				return nil, invalidSkill(name, fmt.Errorf(
					"%w: loaded %q vs requested %q",
					ErrNameMismatch,
					skill.Name,
					name,
				))
			}
			return &skillBundle{source: src, skill: skill}, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("skills: skill %q: %w", name, fs.ErrNotExist)
	}
	return nil, errors.Join(errs...)
}
