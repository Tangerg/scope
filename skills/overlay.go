package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/samber/lo"
)

// Overlay layers several resource sources into one. Earlier sources take
// precedence: on a name collision the first source that has the skill wins, so
// callers express precedence by order (e.g. a project source before a global
// one). The winning source owns the complete skill bundle; missing resources
// do not fall through to a lower-precedence copy with the same name.
// Discovery resolves name ownership through bounded metadata lookups: invalid
// higher-precedence metadata never advertises a lower-precedence copy.
//
// Nil and typed-nil sources are dropped. Overlay of none yields an empty source
// (List returns nothing, Load reports not found).
func Overlay(sources ...ResourceSource) ResourceSource {
	kept := make([]ResourceSource, 0, len(sources))
	for _, s := range sources {
		if !lo.IsNil(s) {
			kept = append(kept, s)
		}
	}
	return &overlaySource{sources: kept}
}

type overlaySource struct {
	sources []ResourceSource
}

var _ Source = (*overlaySource)(nil)
var _ ResourceSource = (*overlaySource)(nil)

// List preserves level-one discovery. It reuses listed summaries and checks
// only higher-precedence sources that omitted a name, because those sources
// may own a malformed bundle. Full-document validation remains with Load.
func (o *overlaySource) List(ctx context.Context) ([]Summary, error) {
	if err := contextError(ctx, "list"); err != nil {
		return nil, err
	}
	var names []string
	type candidate struct {
		summary     Summary
		sourceIndex int
	}
	seen := make(map[string]candidate)
	for sourceIndex, src := range o.sources {
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
			seen[summary.Name] = candidate{summary: summary, sourceIndex: sourceIndex}
			names = append(names, summary.Name)
		}
	}
	slices.Sort(names)
	var out []Summary
	for _, name := range names {
		if err := contextError(ctx, "list"); err != nil {
			return nil, err
		}
		listed := seen[name]
		summary := listed.summary
		if listed.sourceIndex != 0 {
			higher := overlaySource{sources: o.sources[:listed.sourceIndex]}
			owned, err := higher.Lookup(ctx, name)
			if ctxErr := contextError(ctx, "list"); ctxErr != nil {
				return nil, errors.Join(err, ctxErr)
			}
			switch {
			case err == nil:
				summary = owned
			case errors.Is(err, ErrSkillNotFound):
			default:
				return nil, fmt.Errorf("skills: list: %w", err)
			}
		}
		out = append(out, summary)
	}
	return out, nil
}

func (o *overlaySource) Lookup(ctx context.Context, name string) (Summary, error) {
	if err := ValidateName(name); err != nil {
		return Summary{}, err
	}
	operation := fmt.Sprintf("lookup %q", name)
	return o.resolve(ctx, name, operation, func(src ResourceSource) (Summary, error) {
		summary, err := src.Lookup(ctx, name)
		if ctxErr := contextError(ctx, operation); ctxErr != nil {
			return Summary{}, errors.Join(err, ctxErr)
		}
		if err != nil {
			return Summary{}, err
		}
		if err := summary.Validate(); err != nil {
			return Summary{}, invalidSkill(name, err)
		}
		if summary.Name != name {
			return Summary{}, invalidSkill(name, ErrNameMismatch)
		}
		return summary, nil
	})
}

// Load returns the skill from the first source that has it. Missing skills are
// skipped; malformed skills return immediately so a broken higher-precedence
// copy is not silently masked by a lower one.
func (o *overlaySource) Load(ctx context.Context, name string) (*Skill, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("load %q", name)
	return o.resolve(ctx, name, operation, func(src ResourceSource) (*Skill, error) {
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
func (o *overlaySource) OpenResource(ctx context.Context, name, resource string) (fs.File, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if err := validateResourcePath(resource); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("open resource %q/%q", name, resource)
	return o.resolve(ctx, name, operation, func(src ResourceSource) (fs.File, error) {
		file, err := src.OpenResource(ctx, name, resource)
		return receivedResourceFile(ctx, operation, name, resource, file, err)
	})
}

// resolve only falls through when the skill itself is absent. A missing file
// never allows a lower-precedence source to supply part of the winning bundle.
func (o *overlaySource) resolve[T any](ctx context.Context, name, operation string, lookup func(ResourceSource) (T, error)) (T, error) {
	var zero T
	if err := contextError(ctx, operation); err != nil {
		return zero, err
	}
	var errs []error
	for _, src := range o.sources {
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
