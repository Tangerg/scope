package repoarch

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRepositoryUsesOnlyCanonicalScopeIdentity(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	command := exec.Command("git", "-C", root, "ls-files", "-z")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list tracked repository files: %v", err)
	}

	type retiredIdentity struct {
		name string
		word *regexp.Regexp
	}
	retiredIdentities := []retiredIdentity{
		{name: "ly" + "nx", word: regexp.MustCompile(`(?i)\b` + "ly" + "nx" + `\b`)},
		{name: "agent" + "2", word: regexp.MustCompile(`(?i)\b` + "agent" + "2" + `\b`)},
	}
	for relative := range strings.SplitSeq(string(output), "\x00") {
		if relative == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			t.Errorf("read tracked file %q: %v", relative, err)
			continue
		}
		for _, identity := range retiredIdentities {
			if strings.Contains(strings.ToLower(filepath.ToSlash(relative)), identity.name) {
				t.Errorf("tracked path %q uses retired repository identity %q", relative, identity.name)
			}
			if utf8.Valid(data) && identity.word.Match(data) {
				t.Errorf("tracked file %q uses retired repository identity %q", relative, identity.name)
			}
		}
	}
}
