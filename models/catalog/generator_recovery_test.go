package catalog_test

import (
	jsonv2 "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratorStartsWithInvalidEmbeddedCatalog(t *testing.T) {
	directory := t.TempDir()
	invalid := filepath.Join(directory, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"invalid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	configs, err := filepath.Glob("configs/*.json")
	if err != nil || len(configs) == 0 {
		t.Fatalf("embedded configs: %v, %v", configs, err)
	}
	source, err := filepath.Abs(configs[0])
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := jsonv2.Marshal(struct {
		Replace map[string]string
	}{Replace: map[string]string{source: invalid}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(directory, "overlay.json")
	if writeErr := os.WriteFile(overlayPath, overlay, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	command := exec.CommandContext(t.Context(), "go", "run", "-overlay="+overlayPath, "./cmd/generate", "-help")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generator could not start with stale output: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "-source") || !strings.Contains(string(output), "-ladders") {
		t.Fatalf("generator did not reach flag parsing:\n%s", output)
	}
}
