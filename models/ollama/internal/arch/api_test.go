package arch_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Tangerg/scope/models/ollama"
)

type configValidator interface {
	Validate() error
}

func TestChatConstructorsCompile(t *testing.T) {
	t.Parallel()
	var (
		_ func(context.Context, ollama.ChatConfig) (*ollama.Chat, error) = ollama.NewChat
	)
}

func TestChatConfigsValidate(t *testing.T) {
	t.Parallel()
	var (
		_ configValidator = ollama.ChatConfig{}
	)
}

func TestClientModuleDoesNotImportTheDaemonRepository(t *testing.T) {
	t.Parallel()
	module, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if strings.Contains(string(module), "github.com/ollama/ollama") {
		t.Fatal("client adapter imports the Ollama daemon repository")
	}
}
