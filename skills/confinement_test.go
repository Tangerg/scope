package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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

	repository, err := NewDirectoryRepository(root, RepositoryConfig{})
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

	repository, err := NewDirectoryRepository(root, RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ReadResource(t.Context(), repository, "folder-skill", "references", DefaultMaxResourceBytes)
	if !errors.Is(err, ErrResourceNotRegular) && err == nil {
		t.Fatal("a directory was served as a resource")
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

	first, err := NewDirectoryRepository(winner, RepositoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDirectoryRepository(loser, RepositoryConfig{})
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
	repository, err := NewDirectoryRepository(root, RepositoryConfig{})
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
