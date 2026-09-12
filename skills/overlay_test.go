package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func skillFile(name, desc, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body)}
}

type cancelAfterListSource struct {
	ResourceSource
	cancel context.CancelFunc
}

func (c cancelAfterListSource) List(ctx context.Context) ([]Summary, error) {
	summaries, err := c.ResourceSource.List(ctx)
	c.cancel()
	return summaries, err
}

type cancelAfterLoadSource struct {
	ResourceSource
	cancel context.CancelFunc
}

type cancelAfterLookupSource struct {
	ResourceSource
	cancel context.CancelFunc
}

func (c cancelAfterLookupSource) Lookup(ctx context.Context, name string) (Summary, error) {
	summary, err := c.ResourceSource.Lookup(ctx, name)
	c.cancel()
	return summary, err
}

func (c cancelAfterLoadSource) Load(ctx context.Context, name string) (*Skill, error) {
	skill, err := c.ResourceSource.Load(ctx, name)
	c.cancel()
	return skill, err
}

type cancelAfterOpenSource struct {
	ResourceSource
	cancel context.CancelFunc
}

type modelSource struct {
	ResourceSource
	summaries []Summary
	skill     *Skill
}

func (m modelSource) List(context.Context) ([]Summary, error) {
	return m.summaries, nil
}

func (m modelSource) Load(context.Context, string) (*Skill, error) {
	return m.skill, nil
}

func (c cancelAfterOpenSource) OpenResource(ctx context.Context, name, resource string) (fs.File, error) {
	file, err := c.ResourceSource.OpenResource(ctx, name, resource)
	c.cancel()
	return file, err
}

func TestOverlayPrecedence(t *testing.T) {
	project := mustNewFS(fstest.MapFS{
		"shared/SKILL.md":    skillFile("shared", "PROJECT copy", "project body"),
		"only-proj/SKILL.md": skillFile("only-proj", "project only", "x"),
	})
	global := mustNewFS(fstest.MapFS{
		"shared/SKILL.md":    skillFile("shared", "GLOBAL copy", "global body"),
		"only-glob/SKILL.md": skillFile("only-glob", "global only", "y"),
	})

	src := Overlay(project, global) // project first → higher precedence

	list, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d summaries, want 3 (union, deduped): %v", len(list), list)
	}
	want := []Summary{
		{Name: "only-glob", Description: "global only"},
		{Name: "only-proj", Description: "project only"},
		{Name: "shared", Description: "PROJECT copy"},
	}
	if !slices.Equal(list, want) {
		t.Fatalf("List = %v, want %v", list, want)
	}

	// The shared name must resolve to the project copy, not the global one.
	sk, err := src.Load(context.Background(), "shared")
	if err != nil {
		t.Fatalf("Load shared: %v", err)
	}
	if sk.Description != "PROJECT copy" {
		t.Errorf("shared description = %q, want the project copy (precedence)", sk.Description)
	}

	// A global-only skill is still reachable through the overlay.
	if _, err := src.Load(context.Background(), "only-glob"); err != nil {
		t.Errorf("Load only-glob via overlay: %v", err)
	}
}

func TestOverlaidDiscoveryReadsMetadataOnly(t *testing.T) {
	for _, sourceCount := range []int{1, 2} {
		t.Run(fmt.Sprintf("sources_%d", sourceCount), func(t *testing.T) {
			primary := &countingFS{FS: fstest.MapFS{"shared/SKILL.md": skillFile("shared", "primary", strings.Repeat("body", 4096))}}
			secondary := &countingFS{FS: fstest.MapFS{
				"shared/SKILL.md":    skillFile("shared", "secondary", strings.Repeat("body", 4096)),
				"secondary/SKILL.md": skillFile("secondary", "secondary", strings.Repeat("body", 4096)),
			}}
			var sources []ResourceSource
			for _, backing := range []*countingFS{primary, secondary}[:sourceCount] {
				repository, err := NewRepository(backing, RepositoryConfig{MaxFrontmatterBytes: 256, MaxSkillBytes: 512})
				if err != nil {
					t.Fatal(err)
				}
				sources = append(sources, repository)
			}
			source := Overlay(sources...)
			summaries, err := source.List(t.Context())
			if err != nil || len(summaries) != sourceCount {
				t.Fatalf("List = %v, %v", summaries, err)
			}
			if primary.skillOpens != sourceCount || secondary.skillOpens != 2*(sourceCount-1) {
				t.Fatalf("discovery repeated reads: primary=%d secondary=%d", primary.skillOpens, secondary.skillOpens)
			}
			if primary.reads > 257 || secondary.reads > 514 {
				t.Fatalf("discovery read beyond frontmatter: %d/%d", primary.reads, secondary.reads)
			}
			summary, err := source.Lookup(t.Context(), "shared")
			if err != nil || summary.Description != "primary" {
				t.Fatalf("Lookup = %v, %v", summary, err)
			}
			if _, err := source.Load(t.Context(), "shared"); !errors.Is(err, ErrContentTooLarge) {
				t.Fatalf("full loading did not enforce its own limit: %v", err)
			}
		})
	}
}

func TestOverlayDoesNotReplaceBundleMissingMetadata(t *testing.T) {
	primary := mustNewFS(fstest.MapFS{"shared/resource.txt": {Data: []byte("primary")}})
	secondary := mustNewFS(fstest.MapFS{
		"shared/SKILL.md":     skillFile("shared", "secondary", "body"),
		"shared/resource.txt": {Data: []byte("secondary")},
	})
	source := Overlay(primary, secondary)
	for _, lookup := range []func() error{
		func() error { _, err := source.List(t.Context()); return err },
		func() error { _, err := source.Lookup(t.Context(), "shared"); return err },
		func() error { _, err := source.Load(t.Context(), "shared"); return err },
		func() error { _, err := source.OpenResource(t.Context(), "shared", "resource.txt"); return err },
	} {
		if err := lookup(); !errors.Is(err, ErrInvalidSkill) || errors.Is(err, ErrSkillNotFound) {
			t.Fatalf("broken bundle lost ownership: %v", err)
		}
	}
}

func TestOverlaidDiscoveryPreservesCancellationDuringOwnershipLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	primary := cancelAfterLookupSource{ResourceSource: mustNewFS(fstest.MapFS{}), cancel: cancel}
	secondary := mustNewFS(fstest.MapFS{"shared/SKILL.md": skillFile("shared", "secondary", "body")})
	_, err := Overlay(primary, secondary).List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ownership lookup hid cancellation as absence: %v", err)
	}
}

func TestOverlayDropsNilSources(t *testing.T) {
	only := mustNewFS(fstest.MapFS{})
	var typedNil *panicResourceSource
	for _, source := range []ResourceSource{
		Overlay(only),
		Overlay(nil, typedNil, only, typedNil, nil),
	} {
		got, err := source.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("List = %v, want empty", got)
		}
	}
}

func TestOverlayRejectsInvalidSourceModels(t *testing.T) {
	t.Run("summary", func(t *testing.T) {
		source := modelSource{summaries: []Summary{{Name: "broken"}}}
		_, err := Overlay(source).List(t.Context())
		if !errors.Is(err, ErrInvalidSkill) || !errors.Is(err, ErrDescriptionEmpty) {
			t.Fatalf("List error = %v, want invalid empty-description summary", err)
		}
	})

	t.Run("nil skill", func(t *testing.T) {
		_, err := Overlay(modelSource{}).Load(t.Context(), "broken")
		if !errors.Is(err, ErrInvalidSkill) || !errors.Is(err, ErrNilSkill) {
			t.Fatalf("Load error = %v, want invalid nil skill", err)
		}
	})

	t.Run("name mismatch", func(t *testing.T) {
		skill := &Skill{}
		skill.Frontmatter = Frontmatter{Name: "another", Description: "valid description"}
		source := modelSource{skill: skill}
		_, err := Overlay(source).Load(t.Context(), "broken")
		if !errors.Is(err, ErrInvalidSkill) || !errors.Is(err, ErrNameMismatch) {
			t.Fatalf("Load error = %v, want invalid mismatched skill", err)
		}
	})
}

// TestListMissingDir proves a source pointed at a non-existent directory lists
// empty rather than failing — the case behind a project/global skills dir that
// the user hasn't created.
func TestListMissingDir(t *testing.T) {
	repository, err := NewDirectoryRepository("/no/such/skills/dir", RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repository.List(context.Background())
	if err != nil {
		t.Fatalf("List of a missing dir should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %v", got)
	}
}

// TestOverlayReadResource proves resources are served from the first source
// that can satisfy it: the project copy of a shared skill wins, and a
// global-only skill's resource is still reachable through the overlay.
func TestOverlayReadResource(t *testing.T) {
	project := mustNewFS(fstest.MapFS{
		"shared/SKILL.md":           skillFile("shared", "project shared", "x"),
		"shared/references/note.md": {Data: []byte("PROJECT note")},
	})
	global := mustNewFS(fstest.MapFS{
		"shared/SKILL.md":           skillFile("shared", "global shared", "y"),
		"shared/references/note.md": {Data: []byte("GLOBAL note")},
		"glob-only/SKILL.md":        skillFile("glob-only", "global only", "z"),
		"glob-only/assets/data.txt": {Data: []byte("global asset")},
	})
	src := Overlay(project, global)

	note, _, err := ReadResource(context.Background(), src, "shared", "references/note.md", DefaultMaxResourceBytes)
	if err != nil {
		t.Fatalf("ReadResource shared: %v", err)
	}
	if string(note) != "PROJECT note" {
		t.Errorf("shared resource = %q, want the project copy (precedence)", note)
	}

	asset, _, err := ReadResource(context.Background(), src, "glob-only", "assets/data.txt", DefaultMaxResourceBytes)
	if err != nil {
		t.Fatalf("ReadResource glob-only: %v", err)
	}
	if string(asset) != "global asset" {
		t.Errorf("glob-only resource = %q, want the global copy", asset)
	}
}

func TestOverlayKeepsResourcesWithWinningSkill(t *testing.T) {
	project := mustNewFS(fstest.MapFS{
		"shared/SKILL.md": skillFile("shared", "project shared", "project body without resource"),
	})
	global := mustNewFS(fstest.MapFS{
		"shared/SKILL.md":           skillFile("shared", "global shared", "global body"),
		"shared/references/note.md": {Data: []byte("GLOBAL note")},
	})

	_, _, err := ReadResource(t.Context(), Overlay(project, global), "shared", "references/note.md", DefaultMaxResourceBytes)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadResource error = %v, want project resource not found", err)
	}
}

func TestOverlayDoesNotMaskMalformedWinningSkill(t *testing.T) {
	project := mustNewFS(fstest.MapFS{
		"shared/SKILL.md": {Data: []byte("---\nname: shared\ndescription: \n---\nbroken")},
	})
	global := mustNewFS(fstest.MapFS{
		"shared/SKILL.md":           skillFile("shared", "global shared", "global body"),
		"shared/references/note.md": {Data: []byte("GLOBAL note")},
	})
	for _, source := range []ResourceSource{Overlay(project, global), Overlay(Overlay(project), global)} {
		summaries, listErr := source.List(t.Context())
		if len(summaries) != 0 || !errors.Is(listErr, ErrInvalidSkill) || !errors.Is(listErr, ErrDescriptionEmpty) {
			t.Fatalf("List = %v, %v; want malformed winning skill diagnostic", summaries, listErr)
		}
		if _, loadErr := source.Load(t.Context(), "shared"); !errors.Is(loadErr, ErrInvalidSkill) {
			t.Fatalf("Load = %v; want malformed winning skill", loadErr)
		}
	}

	_, _, err := ReadResource(t.Context(), Overlay(project, global), "shared", "references/note.md", DefaultMaxResourceBytes)
	if !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("ReadResource error = %v, want ErrInvalidSkill from project skill", err)
	}
	if !errors.Is(err, ErrDescriptionEmpty) {
		t.Fatalf("ReadResource error = %v, want ErrDescriptionEmpty from project skill", err)
	}
}

func TestOverlayTreatsEmptyOverlaidSourceAsNotFound(t *testing.T) {
	global := mustNewFS(fstest.MapFS{
		"global-skill/SKILL.md": skillFile("global-skill", "global skill", "body"),
	})

	skill, err := Overlay(Overlay(), global).Load(t.Context(), "global-skill")
	if err != nil {
		t.Fatalf("Load after empty overlaid source: %v", err)
	}
	if skill.Name != "global-skill" {
		t.Fatalf("loaded skill = %q, want global-skill", skill.Name)
	}
}

func TestOverlayObservesCancellationAfterSourceCalls(t *testing.T) {
	base := mustNewFS(fstest.MapFS{
		"safe-skill/SKILL.md":           skillFile("safe-skill", "safe skill", "body"),
		"safe-skill/references/note.md": {Data: []byte("note")},
	})
	fallback := mustNewFS(fstest.MapFS{})
	tests := []struct {
		name   string
		source func(context.CancelFunc) ResourceSource
		call   func(context.Context, ResourceSource) error
	}{
		{
			name: "list",
			source: func(cancel context.CancelFunc) ResourceSource {
				return cancelAfterListSource{ResourceSource: base, cancel: cancel}
			},
			call: func(ctx context.Context, source ResourceSource) error {
				_, err := source.List(ctx)
				return err
			},
		},
		{
			name: "lookup",
			source: func(cancel context.CancelFunc) ResourceSource {
				return cancelAfterLookupSource{ResourceSource: base, cancel: cancel}
			},
			call: func(ctx context.Context, source ResourceSource) error {
				_, err := source.Lookup(ctx, "safe-skill")
				return err
			},
		},
		{
			name: "load",
			source: func(cancel context.CancelFunc) ResourceSource {
				return cancelAfterLoadSource{ResourceSource: base, cancel: cancel}
			},
			call: func(ctx context.Context, source ResourceSource) error {
				_, err := source.Load(ctx, "safe-skill")
				return err
			},
		},
		{
			name: "open resource",
			source: func(cancel context.CancelFunc) ResourceSource {
				return cancelAfterOpenSource{ResourceSource: base, cancel: cancel}
			},
			call: func(ctx context.Context, source ResourceSource) error {
				_, err := source.OpenResource(ctx, "safe-skill", "references/note.md")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			source := Overlay(test.source(cancel), fallback)
			if err := test.call(ctx, source); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

// TestOverlayNoSources proves the degenerate empty overlay is well-behaved: List
// is empty, and Load reports the standard not-exist category rather than a
// nil/nil result or a string-only private error.
func TestOverlayNoSources(t *testing.T) {
	src := Overlay() // no sources

	if got, err := src.List(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("List on empty overlay = (%v, %v), want (empty, nil)", got, err)
	}

	_, err := src.Load(context.Background(), "anything")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load error = %v, want fs.ErrNotExist", err)
	}
}
