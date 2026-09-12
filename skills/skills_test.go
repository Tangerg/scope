package skills

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

type panicFS struct{}

func (*panicFS) Open(string) (fs.File, error) {
	panic("typed-nil filesystem was used")
}

type panicResourceSource struct{}

func (*panicResourceSource) Lookup(context.Context, string) (Summary, error) {
	panic("unexpected Lookup")
}

func (*panicResourceSource) List(context.Context) ([]Summary, error) {
	panic("typed-nil source was used")
}

func (*panicResourceSource) Load(context.Context, string) (*Skill, error) {
	panic("typed-nil source was used")
}

func (*panicResourceSource) OpenResource(context.Context, string, string) (fs.File, error) {
	panic("typed-nil source was used")
}

type cancelingFS struct {
	fs.FS
	cancel context.CancelFunc
}

func (c cancelingFS) Open(name string) (fs.File, error) {
	file, err := c.FS.Open(name)
	c.cancel()
	return file, err
}

type cancelingResourceSource struct {
	ResourceSource
	cancel context.CancelFunc
}

type failingOpenFS struct {
	fs.FS
	path string
	err  error
}

type countingFS struct {
	fs.FS
	reads      int
	skillOpens int
}

func (c *countingFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, "/"+SkillFile) {
		c.skillOpens++
	}
	file, err := c.FS.Open(name)
	if err != nil || name == "." {
		return file, err
	}
	return &countingFile{File: file, reads: &c.reads}, nil
}

type countingFile struct {
	fs.File
	reads *int
}

func (c *countingFile) Read(buffer []byte) (int, error) {
	read, err := c.File.Read(buffer)
	*c.reads += read
	return read, err
}

func (f failingOpenFS) Open(name string) (fs.File, error) {
	if name == f.path {
		return nil, f.err
	}
	return f.FS.Open(name)
}

func (c cancelingResourceSource) OpenResource(ctx context.Context, name, resource string) (fs.File, error) {
	file, err := c.ResourceSource.OpenResource(ctx, name, resource)
	c.cancel()
	return file, err
}

type nilFileResourceSource struct{ ResourceSource }

func (n nilFileResourceSource) OpenResource(context.Context, string, string) (fs.File, error) {
	return nil, nil
}

const pdfSkill = `---
name: pdf-processing
description: Extract PDF text, fill forms, overlay files. Use when handling PDFs.
license: Apache-2.0
metadata:
  author: example-org
  version: "1.0"
allowed-tools: Bash(git:*) Read
---
# PDF Processing

Step 1. Use the extract script.

See references/REFERENCE.md for details.
`

func newTestFS() ResourceSource {
	return mustNewFS(fstest.MapFS{
		"pdf-processing/SKILL.md":                {Data: []byte(pdfSkill)},
		"pdf-processing/references/REFERENCE.md": {Data: []byte("# Reference\nDetailed notes.")},
		"data-analysis/SKILL.md":                 {Data: []byte("---\nname: data-analysis\ndescription: Analyze data.\n---\nbody")},
		// A directory that is not a valid skill — must be skipped by List.
		"not-a-skill/readme.txt": {Data: []byte("ignore me")},
		"malformed/SKILL.md":     {Data: []byte("missing frontmatter")},
		"UPPER/SKILL.md":         skillFile("UPPER", "invalid directory name", "body"),
	})
}

func mustNewFS(fsys fs.FS) *Repository {
	repository, err := NewRepository(fsys, RepositoryConfig{})
	if err != nil {
		panic(err)
	}
	return repository
}

func TestParse(t *testing.T) {
	skill, err := Parse([]byte(pdfSkill))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if skill.Name != "pdf-processing" {
		t.Errorf("name = %q", skill.Name)
	}
	if skill.License != "Apache-2.0" {
		t.Errorf("license = %q", skill.License)
	}
	if got := skill.Metadata["version"]; got != "1.0" {
		t.Errorf("metadata.version = %q", got)
	}
	if got := skill.AllowedToolList(); len(got) != 2 || got[0] != "Bash(git:*)" {
		t.Errorf("allowed tools = %v", got)
	}
	if skill.Instructions == "" || skill.Instructions[0] != '#' {
		t.Errorf("instructions should start with the markdown heading, got %q", skill.Instructions)
	}
}

func TestParseNormalizesBOMAndCRLF(t *testing.T) {
	skill, err := Parse([]byte("\ufeff---\r\nname: portable-skill\r\ndescription: Portable line endings\r\n---\r\n# Instructions\r\n\r\nUse it.\r\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if skill.Name != "portable-skill" {
		t.Fatalf("name = %q, want portable-skill", skill.Name)
	}
	if skill.Instructions != "# Instructions\n\nUse it." {
		t.Fatalf("instructions = %q", skill.Instructions)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	for _, content := range []string{
		"no front matter here",
		" ---\nname: padded-open\ndescription: invalid fence\n---\nbody",
		"---\nname: padded-close\ndescription: invalid fence\n ---\nbody",
	} {
		if _, err := Parse([]byte(content)); !errors.Is(err, ErrNoFrontmatter) {
			t.Errorf("Parse(%q) error = %v, want ErrNoFrontmatter", content, err)
		}
	}
}

func TestRepositoryResourceRequiresValidOwningSkill(t *testing.T) {
	repository := mustNewFS(fstest.MapFS{
		"broken/SKILL.md":           {Data: []byte("not frontmatter")},
		"broken/references/note.md": {Data: []byte("must not escape invalid bundle")},
	})
	if _, _, err := ReadResource(t.Context(), repository, "broken", "references/note.md", DefaultMaxResourceBytes); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("ReadResource error = %v, want ErrInvalidSkill", err)
	}
}

func TestRepositoryResourceMustBeRegularFile(t *testing.T) {
	repository := mustNewFS(fstest.MapFS{
		"safe-skill/SKILL.md":                skillFile("safe-skill", "safe skill", "body"),
		"safe-skill/references/reference.md": {Data: []byte("reference")},
	})
	if _, _, err := ReadResource(t.Context(), repository, "safe-skill", "references", DefaultMaxResourceBytes); !errors.Is(err, ErrResourceNotRegular) {
		t.Fatalf("ReadResource error = %v, want ErrResourceNotRegular", err)
	}
}

func TestRepositoryPreservesCancellationCause(t *testing.T) {
	want := errors.New("repository stopped")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(want)
	if _, err := newTestFS().List(ctx); !errors.Is(err, want) {
		t.Fatalf("List error = %v, want cancellation cause", err)
	}
}

func TestParseValidatesSkill(t *testing.T) {
	_, err := Parse([]byte("---\nname: invalid-skill\ndescription:\n---\n"))
	if !errors.Is(err, ErrInvalidSkill) || !errors.Is(err, ErrDescriptionEmpty) {
		t.Fatalf("Parse error = %v, want ErrInvalidSkill and ErrDescriptionEmpty", err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		fm   Frontmatter
		want error
	}{
		"ok":            {Frontmatter{Name: "pdf-tools", Description: "do things"}, nil},
		"empty name":    {Frontmatter{Description: "x"}, ErrNameEmpty},
		"upper name":    {Frontmatter{Name: "PDF", Description: "x"}, ErrNameInvalid},
		"padded name":   {Frontmatter{Name: " pdf", Description: "x"}, ErrNameInvalid},
		"double hyphen": {Frontmatter{Name: "a--b", Description: "x"}, ErrNameInvalid},
		"empty desc":    {Frontmatter{Name: "ok"}, ErrDescriptionEmpty},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.fm.Validate()
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestModelValidation(t *testing.T) {
	var skill *Skill
	if err := skill.Validate(); !errors.Is(err, ErrNilSkill) {
		t.Fatalf("nil Skill.Validate error = %v, want ErrNilSkill", err)
	}
	if err := (Summary{Name: "valid-skill"}).Validate(); !errors.Is(err, ErrDescriptionEmpty) {
		t.Fatalf("Summary.Validate error = %v, want ErrDescriptionEmpty", err)
	}
}

func TestSourceRejectsInvalidNamesBeforeFilesystemAccess(t *testing.T) {
	source := mustNewFS(&panicFS{})
	tests := []struct {
		name string
		want error
	}{
		{name: "", want: ErrNameEmpty},
		{name: "UPPER", want: ErrNameInvalid},
		{name: " padded", want: ErrNameInvalid},
		{name: "nested/skill", want: ErrNameInvalid},
		{name: strings.Repeat("a", maxNameLen+1), want: ErrNameTooLong},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := source.Load(t.Context(), test.name); !errors.Is(err, test.want) {
				t.Fatalf("Load(%q) error = %v, want %v", test.name, err, test.want)
			}
		})
	}
}

func TestList(t *testing.T) {
	got, err := newTestFS().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d summaries, want 2 (non-skill dir skipped): %v", len(got), got)
	}
	// Sorted by name: data-analysis before pdf-processing.
	if got[0].Name != "data-analysis" || got[1].Name != "pdf-processing" {
		t.Errorf("summaries not sorted by name: %v", got)
	}
}

func TestListReadsOnlyBoundedFrontmatter(t *testing.T) {
	document := skillFile("large-skill", "large skill", strings.Repeat("body", 64*1024))
	filesystem := &countingFS{FS: fstest.MapFS{"large-skill/SKILL.md": document}}
	repository, err := NewRepository(filesystem, RepositoryConfig{MaxFrontmatterBytes: 1024, MaxSkillBytes: int64(len(document.Data))})
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := repository.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Name != "large-skill" {
		t.Fatalf("summaries = %#v", summaries)
	}
	if filesystem.reads >= len(document.Data) {
		t.Fatalf("List read the complete %d-byte skill document", len(document.Data))
	}
}

func TestListSkipsSkillWithOversizedFrontmatterLine(t *testing.T) {
	filesystem := fstest.MapFS{
		"oversized/SKILL.md": {Data: []byte("---\nname: oversized\ndescription: " + strings.Repeat("x", 1024) + "\n---\nbody")},
		"valid/SKILL.md":     skillFile("valid", "valid skill", "body"),
	}
	repository, err := NewRepository(filesystem, RepositoryConfig{MaxFrontmatterBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := repository.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Name != "valid" {
		t.Fatalf("summaries = %#v", summaries)
	}
}

func TestRepositoryEnforcesEntryAndSkillLimits(t *testing.T) {
	filesystem := fstest.MapFS{
		"one/SKILL.md": skillFile("one", "one", "body"),
		"two/SKILL.md": skillFile("two", "two", "body"),
	}
	repository, err := NewRepository(filesystem, RepositoryConfig{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, listErr := repository.List(t.Context()); !errors.Is(listErr, ErrRepositoryLarge) {
		t.Fatalf("List error = %v, want ErrRepositoryLarge", listErr)
	}

	repository, err = NewRepository(filesystem, RepositoryConfig{MaxFrontmatterBytes: 16, MaxSkillBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, loadErr := repository.Load(t.Context(), "one"); !errors.Is(loadErr, ErrContentTooLarge) {
		t.Fatalf("Load error = %v, want ErrContentTooLarge", loadErr)
	}
	if _, err := NewRepository(filesystem, RepositoryConfig{MaxEntries: -1}); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("NewRepository error = %v, want ErrInvalidLimit", err)
	}
	if _, err := NewRepository(filesystem, RepositoryConfig{MaxSkillBytes: maxBoundedReadBytes + 1}); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("overflowing repository limit error = %v, want ErrInvalidLimit", err)
	}
}

func TestLoad(t *testing.T) {
	sk, err := newTestFS().Load(context.Background(), "pdf-processing")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if sk.Description == "" || sk.Instructions == "" {
		t.Errorf("loaded skill missing description or instructions: %+v", sk)
	}
}

func TestLoadClassifiesInvalidSkillAndPreservesCause(t *testing.T) {
	source := mustNewFS(fstest.MapFS{
		"bad-description/SKILL.md": {Data: []byte("---\nname: bad-description\ndescription:\n---\nbody")},
		"bad-document/SKILL.md":    {Data: []byte("missing frontmatter")},
		"mismatch/SKILL.md":        skillFile("another-name", "mismatched name", "body"),
	})

	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "bad-description", cause: ErrDescriptionEmpty},
		{name: "bad-document", cause: ErrNoFrontmatter},
		{name: "mismatch", cause: ErrNameMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := source.Load(t.Context(), test.name)
			if !errors.Is(err, ErrInvalidSkill) {
				t.Fatalf("Load error = %v, want ErrInvalidSkill", err)
			}
			if !errors.Is(err, test.cause) {
				t.Fatalf("Load error = %v, want %v cause", err, test.cause)
			}
		})
	}

	_, err := source.Load(t.Context(), "missing")
	if !errors.Is(err, ErrSkillNotFound) || !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("Load missing error = %v, want ErrSkillNotFound", err)
	}
}

func TestListReturnsRepositoryReadFailure(t *testing.T) {
	readErr := errors.New("repository read failed")
	base := fstest.MapFS{
		"broken/SKILL.md": skillFile("broken", "broken skill", "body"),
	}
	source := mustNewFS(failingOpenFS{FS: base, path: "broken/SKILL.md", err: readErr})

	_, err := source.List(t.Context())
	if !errors.Is(err, readErr) {
		t.Fatalf("List error = %v, want repository read failure", err)
	}
}

func TestReadResource(t *testing.T) {
	fsrc := newTestFS()

	data, _, err := ReadResource(context.Background(), fsrc, "pdf-processing", "references/REFERENCE.md", DefaultMaxResourceBytes)
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(data) == 0 {
		t.Error("resource content is empty")
	}

	// Traversal out of the skill directory must be rejected.
	if _, _, err := ReadResource(context.Background(), fsrc, "pdf-processing", "../data-analysis/SKILL.md", DefaultMaxResourceBytes); !errors.Is(err, ErrResourcePath) {
		t.Errorf("traversal err = %v, want ErrResourcePath", err)
	}
}

func TestReadResourceReturnsBoundedTruncation(t *testing.T) {
	data, truncated, err := ReadResource(
		t.Context(), newTestFS(), "pdf-processing", "references/REFERENCE.md", 5,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || string(data) != "# Ref" {
		t.Fatalf("ReadResource = (%q, %v), want bounded prefix", data, truncated)
	}
	if _, _, err := ReadResource(t.Context(), newTestFS(), "pdf-processing", "references/REFERENCE.md", 0); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("zero limit error = %v, want ErrInvalidLimit", err)
	}
	if _, _, err := ReadResource(t.Context(), newTestFS(), "pdf-processing", "references/REFERENCE.md", maxBoundedReadBytes+1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("overflowing limit error = %v, want ErrInvalidLimit", err)
	}
}

func TestReadResourceRejectsNilFileWithoutPanicking(t *testing.T) {
	source := nilFileResourceSource{ResourceSource: newTestFS()}
	_, _, err := ReadResource(t.Context(), source, "pdf-processing", "references/REFERENCE.md", DefaultMaxResourceBytes)
	if !errors.Is(err, ErrNilResourceFile) {
		t.Fatalf("ReadResource error = %v, want ErrNilResourceFile", err)
	}
}

func TestResourcePathsArePortableAndRelative(t *testing.T) {
	source := mustNewFS(&panicFS{})
	for _, resource := range []string{
		"",
		".",
		"../other-skill/SKILL.md",
		"references//note.md",
		"references/../note.md",
		`references\..\sibling-skill\SKILL.md`,
		"/absolute/path",
	} {
		t.Run(resource, func(t *testing.T) {
			if _, err := source.OpenResource(t.Context(), "safe-skill", resource); !errors.Is(err, ErrResourcePath) {
				t.Fatalf("OpenResource(%q) error = %v, want ErrResourcePath", resource, err)
			}
		})
	}
}

func TestOperationsHonorCanceledContextBeforeAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	source := mustNewFS(&panicFS{})

	operations := []struct {
		name string
		call func() error
	}{
		{name: "list", call: func() error { _, err := source.List(ctx); return err }},
		{name: "load", call: func() error { _, err := source.Load(ctx, "safe-skill"); return err }},
		{name: "open resource", call: func() error {
			_, err := source.OpenResource(ctx, "safe-skill", "references/note.md")
			return err
		}},
		{name: "read resource", call: func() error {
			_, _, err := ReadResource(ctx, &panicResourceSource{}, "safe-skill", "references/note.md", DefaultMaxResourceBytes)
			return err
		}},
		{name: "empty overlay list", call: func() error { _, err := Overlay().List(ctx); return err }},
		{name: "empty overlay load", call: func() error { _, err := Overlay().Load(ctx, "safe-skill"); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestOperationsHonorCancellationDuringAccess(t *testing.T) {
	base := fstest.MapFS{
		"safe-skill/SKILL.md":           skillFile("safe-skill", "safe skill", "body"),
		"safe-skill/references/note.md": {Data: []byte("note")},
	}
	for _, operation := range []struct {
		name string
		call func(context.Context, ResourceSource) error
	}{
		{name: "list", call: func(ctx context.Context, source ResourceSource) error {
			_, err := source.List(ctx)
			return err
		}},
		{name: "load", call: func(ctx context.Context, source ResourceSource) error {
			_, err := source.Load(ctx, "safe-skill")
			return err
		}},
		{name: "open resource", call: func(ctx context.Context, source ResourceSource) error {
			_, err := source.OpenResource(ctx, "safe-skill", "references/note.md")
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			source := mustNewFS(cancelingFS{FS: base, cancel: cancel})
			if err := operation.call(ctx, source); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	source := cancelingResourceSource{ResourceSource: mustNewFS(base), cancel: cancel}
	if _, _, err := ReadResource(ctx, source, "safe-skill", "references/note.md", DefaultMaxResourceBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadResource error = %v, want context.Canceled", err)
	}
}

func TestReadResourceRejectsNilSource(t *testing.T) {
	var typedNil *panicResourceSource
	tests := []struct {
		name   string
		source ResourceSource
	}{
		{name: "nil", source: nil},
		{name: "typed nil", source: typedNil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ReadResource(t.Context(), test.source, "pdf-processing", "references/REFERENCE.md", DefaultMaxResourceBytes); !errors.Is(err, ErrNilSource) {
				t.Fatalf("ReadResource error = %v, want ErrNilSource", err)
			}
		})
	}
}

func TestDirRejectsResourceSymlinkEscapingRoot(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "safe-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, SkillFile),
		[]byte("---\nname: safe-skill\ndescription: Safe skill.\n---\nbody"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("must not escape"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillDir, "references", "secret.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	repository, err := NewDirectoryRepository(root, RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadResource(t.Context(), repository, "safe-skill", "references/secret.txt", DefaultMaxResourceBytes); err == nil {
		t.Fatal("ReadResource followed a symlink outside the source root")
	}
}

func TestDirRejectsResourceSymlinkEscapingSkill(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "safe-skill")
	otherDir := filepath.Join(root, "other-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, SkillFile),
		[]byte("---\nname: safe-skill\ndescription: Safe skill.\n---\nbody"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "secret.txt"), []byte("sibling secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillDir, "references", "secret.txt")
	if err := os.Symlink(filepath.Join("..", "..", "other-skill", "secret.txt"), link); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	repository, err := NewDirectoryRepository(root, RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadResource(t.Context(), repository, "safe-skill", "references/secret.txt", DefaultMaxResourceBytes); err == nil {
		t.Fatal("ReadResource followed a symlink into a sibling skill")
	}
}

func TestNewFSRejectsNilFilesystemWithoutPanicking(t *testing.T) {
	var typedNil *panicFS
	tests := []struct {
		name       string
		filesystem fs.FS
	}{
		{name: "nil", filesystem: nil},
		{name: "typed nil", filesystem: typedNil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, err := NewRepository(test.filesystem, RepositoryConfig{})
			if repository != nil || !errors.Is(err, ErrNilFilesystem) {
				t.Fatalf("NewRepository = (%v, %v), want (nil, ErrNilFilesystem)", repository, err)
			}
		})
	}
}

func TestRepositoryRejectsZeroValueWithoutPanicking(t *testing.T) {
	for _, repository := range []*Repository{nil, {}} {
		if _, err := repository.List(t.Context()); !errors.Is(err, ErrNilFilesystem) {
			t.Fatalf("List error = %v, want ErrNilFilesystem", err)
		}
	}
}
