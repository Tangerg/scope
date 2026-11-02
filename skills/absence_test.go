package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
)

type failingLookupSource struct {
	ResourceSource
	err error
}

func (f failingLookupSource) Lookup(context.Context, string) (Summary, error) {
	return Summary{}, f.err
}
func (f failingLookupSource) Load(context.Context, string) (*Skill, error) { return nil, f.err }
func (f failingLookupSource) OpenResource(context.Context, string, string) (fs.File, error) {
	return nil, f.err
}

type providerMissing struct{ cause error }

func (p providerMissing) Error() string        { return p.cause.Error() }
func (p providerMissing) Unwrap() error        { return p.cause }
func (p providerMissing) Is(target error) bool { return target == ErrSkillNotFound }

func TestOverlayHonorsSingleScopeAbsenceTranslation(t *testing.T) {
	noRecord := errors.New("no record in backend")
	lower := mustNewFS(fstest.MapFS{"demo/SKILL.md": skillFile("demo", "lower", "text")})
	for _, tc := range []struct {
		name   string
		cause  error
		denied bool
	}{
		{name: "wrapped backend absence", cause: fmt.Errorf("lookup: %w", providerMissing{noRecord})},
		{name: "single cause join", cause: providerMissing{errors.Join(noRecord)}},
		{name: "mixed causes below translator", cause: providerMissing{errors.Join(errors.Join(providerMissing{noRecord}, fs.ErrPermission))}, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upper := failingLookupSource{ResourceSource: Overlay(), err: tc.cause}
			s, err := Overlay(upper, lower).Load(t.Context(), "demo")
			if tc.denied {
				if s != nil || !errors.Is(err, fs.ErrPermission) {
					t.Fatalf("independent access failure hidden: %v, %v", s, err)
				}
				return
			}
			if err != nil || s == nil || s.Description != "lower" {
				t.Fatalf("translated absence blocked lower source: %v, %v", s, err)
			}
		})
	}
}

func TestIndependentAccessFailurePreventsSkillFallback(t *testing.T) {
	denied := fmt.Errorf("upstream: %w", errors.Join(ErrSkillNotFound, fs.ErrPermission))
	higher := failingLookupSource{ResourceSource: Overlay(), err: denied}
	lower := mustNewFS(fstest.MapFS{
		"demo/SKILL.md": skillFile("demo", "lower", "text"),
		"demo/note":     {Data: []byte("text")},
	})
	src := Overlay(higher, lower)
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"list", func() error { _, err := src.List(ctx); return err }},
		{"lookup", func() error { _, err := src.Lookup(ctx, "demo"); return err }},
		{"load", func() error { _, err := src.Load(ctx, "demo"); return err }},
		{"open", func() error {
			f, err := src.OpenResource(ctx, "demo", "note")
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("lost higher source access failure: %v", err)
			}
		})
	}
	for _, path := range []string{"", "note"} {
		ref, err := NewReference("demo", path, []byte("text"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := ref.Resolve(ctx, higher, 256)
		if data != nil || !errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrUnresolvedReference) {
			t.Errorf("reference %q classified mixed failure as absence: %q, %v", path, data, err)
		}
	}
}

type failedOpenFS func(string) (fs.File, error)

func (f failedOpenFS) Open(name string) (fs.File, error) { return f(name) }

func TestRepositoryRequiresDefiniteAbsence(t *testing.T) {
	missing := func(path string) error { return &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist} }
	for _, failurePath := range []string{".", "demo/SKILL.md", "demo", ""} {
		t.Run("mixed at "+failurePath, func(t *testing.T) {
			repo := mustNewFS(failedOpenFS(func(path string) (fs.File, error) {
				cause := missing(path)
				if path == failurePath {
					cause = errors.Join(cause, fs.ErrPermission)
				}
				return nil, fmt.Errorf("source: %w", cause)
			}))
			ctx := t.Context()
			_, err := repo.List(ctx)
			if (failurePath == ".") != errors.Is(err, fs.ErrPermission) {
				t.Fatalf("root access error: %v", err)
			}
			_, err = repo.Load(ctx, "demo")
			denied := failurePath == "demo" || failurePath == "demo/SKILL.md"
			if errors.Is(err, fs.ErrPermission) != denied || errors.Is(err, ErrSkillNotFound) == denied {
				t.Fatalf("bundle absence: %v", err)
			}
			if !denied {
				lower := mustNewFS(fstest.MapFS{"demo/SKILL.md": skillFile("demo", "lower", "text")})
				if skill, err := Overlay(repo, lower).Load(ctx, "demo"); err != nil || skill.Description != "lower" {
					t.Fatalf("wrapped absence blocked lower source: %v, %v", skill, err)
				}
			}
		})
	}
}

func TestInstructionReferencePreservesCancellation(t *testing.T) {
	ref, err := NewReference("demo", "", []byte("text"))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("authorization revoked")
	valid := mustNewFS(fstest.MapFS{"demo/SKILL.md": skillFile("demo", "example", "text")})
	for _, tc := range []struct {
		name    string
		source  ResourceSource
		before  bool
		failure error
	}{
		{name: "before load", source: new(panicResourceSource), before: true},
		{name: "returned content", source: valid},
		{name: "returned failure", source: failingLookupSource{err: fs.ErrPermission}, failure: fs.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			if tc.before {
				cancel(cause)
			}
			source := cancelAfterLoadSource{ResourceSource: tc.source, cancel: func() { cancel(cause) }}
			data, err := ref.Resolve(ctx, source, 256)
			if data != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || tc.failure != nil && !errors.Is(err, tc.failure) {
				t.Fatalf("resolve returned bytes or lost a failure: %q, %v", data, err)
			}
		})
	}
}
