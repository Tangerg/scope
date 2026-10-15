package repoarch

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExamplesConsumePublishedModules(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	consumer := discoverModules(t, root)["github.com/Tangerg/scope/examples"]
	if consumer.file == nil {
		t.Fatal("missing independent examples module")
	}
	if len(consumer.file.Replace) != 0 {
		t.Fatal("examples must consume published modules without replacements")
	}
	args := []string{"list", "-m", "-f", "{{.Path}} {{.Version}} {{.Main}}{{if .Replace}} replaced{{end}}"}
	var expected []string
	for _, requirement := range consumer.file.Require {
		if !strings.HasPrefix(requirement.Mod.Path, "github.com/Tangerg/scope/") {
			continue
		}
		args = append(args, requirement.Mod.Path)
		expected = append(expected, requirement.Mod.Path+" "+requirement.Mod.Version+" false")
	}
	if len(expected) == 0 {
		t.Fatal("examples have no published Scope consumers")
	}
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = filepath.Join(root, "examples")
	command.Env = append(command.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve published consumer dependencies: %v\n%s", err, output)
	}
	actual := strings.Split(strings.TrimSpace(string(output)), "\n")
	if !slices.Equal(actual, expected) {
		t.Fatalf("selected consumer modules=%q, want %q", actual, expected)
	}
}

func TestOrdinaryConsumerLoadsOnlyCore(t *testing.T) {
	t.Parallel()
	command := exec.CommandContext(t.Context(), "go", "list", "-deps", "-test", "-f", "{{.ImportPath}}", "./chat")
	command.Dir = filepath.Join(repositoryRoot(t), "examples")
	command.Env = append(command.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve ordinary consumer dependencies: %v\n%s", err, output)
	}
	seenClient := false
	for line := range strings.Lines(string(output)) {
		dependency := strings.TrimSpace(line)
		if dependency == "github.com/Tangerg/scope/core/chatclient" {
			seenClient = true
		}
		if strings.HasPrefix(dependency, "github.com/Tangerg/scope/") &&
			!strings.HasPrefix(dependency, "github.com/Tangerg/scope/core/") &&
			!strings.HasPrefix(dependency, "github.com/Tangerg/scope/examples/chat") {
			t.Errorf("ordinary consumer or tests load a non-Core dependency: %s", dependency)
		}
	}
	if !seenClient {
		t.Fatal("ordinary consumer no longer exercises chatclient")
	}
}
