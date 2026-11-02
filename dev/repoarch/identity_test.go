package repoarch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repositoryTitle = "# Scope"

func TestRepositoryPublishesCanonicalIdentity(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	if len(discoverModules(t, root)) == 0 {
		t.Fatal("no independently named modules were discovered")
	}
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	title, _, _ := strings.Cut(string(data), "\n")
	if title != repositoryTitle {
		t.Errorf("repository entry title = %q, want %q", title, repositoryTitle)
	}
}
