package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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
// Nil and typed-nil sources are dropped; an empty overlay lists nothing and
// reports every skill as not found.
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
	listed, err := o.listFirstSeen(ctx)
	if err != nil {
		return nil, err
	}
	names := slices.Sorted(maps.Keys(listed))
	var out []Summary
	for _, name := range names {
		summary, err := o.ownedSummary(ctx, listed[name])
		if err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, nil
}

type listedSummary struct {
	summary     Summary
	sourceIndex int
}

func (o *overlaySource) listFirstSeen(ctx context.Context) (map[string]listedSummary, error) {
	if err := contextError(ctx, "list"); err != nil {
		return nil, err
	}
	listed := make(map[string]listedSummary)
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
			if _, dup := listed[summary.Name]; !dup {
				listed[summary.Name] = listedSummary{summary: summary, sourceIndex: sourceIndex}
			}
		}
	}
	return listed, nil
}

func (o *overlaySource) ownedSummary(ctx context.Context, listed listedSummary) (Summary, error) {
	if err := contextError(ctx, "list"); err != nil {
		return Summary{}, err
	}
	if listed.sourceIndex == 0 {
		return listed.summary, nil
	}
	higher := overlaySource{sources: o.sources[:listed.sourceIndex]}
	owned, err := higher.Lookup(ctx, listed.summary.Name)
	if ctxErr := contextError(ctx, "list"); ctxErr != nil {
		return Summary{}, errors.Join(err, ctxErr)
	}
	switch {
	case err == nil:
		return owned, nil
	case errors.Is(err, ErrSkillNotFound):
		return listed.summary, nil
	default:
		return Summary{}, fmt.Errorf("skills: list: %w", err)
	}
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

// Load returns immediately on a malformed skill so a broken higher-precedence
// copy is not masked by a lower one.
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
