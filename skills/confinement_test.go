package skills

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func directoryRoot(t testing.TB, path string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func writeSkill(t *testing.T, root, name string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: A skill for tests.\n---\nbody"
	if err := os.WriteFile(filepath.Join(directory, SkillFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestDirResourceOpenFailsWhenTheSkillDirectoryIsGone(t *testing.T) {
	root := t.TempDir()
	directory := writeSkill(t, root, "vanishing-skill")

	repository, err := NewDirectoryRepository(directoryRoot(t, root), RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadResource(t.Context(), repository, "vanishing-skill", "references/a.md", DefaultMaxResourceBytes); err == nil {
		t.Fatal("a missing resource opened successfully")
	}

	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadResource(t.Context(), repository, "vanishing-skill", "references/a.md", DefaultMaxResourceBytes); err == nil {
		t.Fatal("a removed skill directory opened successfully")
	}
}

func TestDirResourceRejectsADirectoryTarget(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "folder-skill")

	repository, err := NewDirectoryRepository(directoryRoot(t, root), RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ReadResource(t.Context(), repository, "folder-skill", "references", DefaultMaxResourceBytes)
	if !errors.Is(err, ErrResourceNotRegular) {
		t.Fatalf("directory resource error = %v, want ErrResourceNotRegular", err)
	}
}

func TestOverlayOpensResourcesFromTheWinningSource(t *testing.T) {
	winner := t.TempDir()
	loser := t.TempDir()
	writeSkill(t, winner, "shared-skill")
	writeSkill(t, loser, "shared-skill")

	if err := os.WriteFile(filepath.Join(loser, "shared-skill", "references", "only-here.md"), []byte("shadowed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(winner, "shared-skill", "references", "winner.md"), []byte("winner"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := NewDirectoryRepository(directoryRoot(t, winner), RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDirectoryRepository(directoryRoot(t, loser), RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	overlaid := Overlay(first, second)

	content, truncated, err := ReadResource(t.Context(), overlaid, "shared-skill", "references/winner.md", DefaultMaxResourceBytes)
	if err != nil || truncated || string(content) != "winner" {
		t.Fatalf("winning resource = %q, truncated %t, %v", content, truncated, err)
	}

	if _, _, err := ReadResource(t.Context(), overlaid, "shared-skill", "references/only-here.md", DefaultMaxResourceBytes); err == nil {
		t.Fatal("a shadowed source contributed a resource to the winning skill")
	}
}

func TestOverlayValidatesBeforeResolving(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "valid-skill")
	repository, err := NewDirectoryRepository(directoryRoot(t, root), RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	overlaid := Overlay(repository)

	if _, err := overlaid.OpenResource(t.Context(), "Invalid Name", "references/a.md"); err == nil {
		t.Fatal("OpenResource accepted an invalid skill name")
	}
	for name, resource := range map[string]string{
		"current directory": ".",
		"parent escape":     "../secret.txt",
		"absolute":          "/etc/passwd",
		"backslash":         `references\a.md`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := overlaid.OpenResource(t.Context(), "valid-skill", resource); !errors.Is(err, ErrResourcePath) {
				t.Fatalf("OpenResource(%q) error = %v, want ErrResourcePath", resource, err)
			}
		})
	}
}

func TestDirectoryAuthorityCannotMoveToAReplacedPath(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "selected")
	other := filepath.Join(parent, "unselected")
	legitimate := writeSkill(t, root, "guide")
	if err := os.WriteFile(filepath.Join(legitimate, "references/initial.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	foreign := writeSkill(t, other, "guide")
	if err := os.WriteFile(filepath.Join(foreign, "references/private.txt"), []byte("other grant"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewDirectoryRepository(directoryRoot(t, root), RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(root, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(other, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	b, _, err := ReadResource(t.Context(), source, "guide", "references/private.txt", 128)
	if err == nil || len(b) != 0 {
		t.Fatalf("old grant read replacement content: %q %v", b, err)
	}
	b, _, err = ReadResource(t.Context(), source, "guide", "references/initial.txt", 128)
	if err != nil || string(b) != "original" {
		t.Fatalf("renaming revoked the original authority: %q %v", b, err)
	}
}

func TestBorrowedDirectoryRejectsNewOperationsAfterHostClose(t *testing.T) {
	if repository, err := NewDirectoryRepository(nil, RepositoryConfig{}); repository != nil || !errors.Is(err, ErrNilFilesystem) {
		t.Fatalf("nil directory: %v %v", repository, err)
	}
	dir := t.TempDir()
	bundle := writeSkill(t, dir, "guide")
	if err := os.WriteFile(filepath.Join(bundle, "notes"), []byte("open file"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	source, err := NewDirectoryRepository(root, RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := source.OpenResource(t.Context(), "guide", "notes")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Lookup(t.Context(), "guide"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("revoked root: %v", err)
	}
	if repository, closedErr := NewDirectoryRepository(root, RepositoryConfig{}); repository != nil || !errors.Is(closedErr, os.ErrClosed) {
		t.Fatalf("admitted closed directory: %v %v", repository, closedErr)
	}
	b, err := io.ReadAll(file)
	if err != nil || string(b) != "open file" {
		t.Fatalf("acquired file: %q %v", b, err)
	}
}
