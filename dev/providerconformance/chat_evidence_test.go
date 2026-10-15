package providerconformance_test

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/dev/providerconformance/internal/chatcheck"
)

func TestChatEvidence(t *testing.T) {
	output := os.Getenv(chatcheck.OutputEnv)
	if output == "" {
		t.Skip("evidence generation requires an explicit SCOPE_TEST_CHAT_EVIDENCE_OUTPUT destination")
	}
	if err := runChatEvidence(t.Context(), repositoryRoot(t), output, os.Getenv(chatcheck.LiveSelectionEnv)); err != nil {
		t.Fatal(err)
	}
}

func runChatEvidence(parent context.Context, root, output, live string) error {
	selected, selectionErr := chatcheck.Select(live)
	if selectionErr != nil {
		return selectionErr
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Minute)
	defer cancel()
	revisionCommand := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	revisionCommand.Dir = root
	revision, revisionErr := revisionCommand.Output()
	if revisionErr != nil {
		return revisionErr
	}
	statusCommand := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCommand.Dir = root
	status, statusErr := statusCommand.Output()
	if statusErr != nil {
		return statusErr
	}
	var evidence []chatcheck.Evidence
	var failures []error
	for _, entry := range chatcheck.Plan() {
		names := make([]string, 0, len(entry.Tests))
		for _, scenario := range entry.Tests {
			names = append(names, regexp.QuoteMeta(scenario.Test))
		}
		results, testErr := runChatEvidenceTests(ctx, filepath.Join(root, entry.Directory), "^("+strings.Join(names, "|")+")$", "")
		if testErr != nil {
			failures = append(failures, fmt.Errorf("%s offline: %w", entry.ID, testErr))
		}
		for _, scenario := range entry.Tests {
			row := results.Evidence(entry.ID, "offline_protocol", "", scenario)
			row.Directory = entry.Directory
			evidence = append(evidence, row)
			if row.Status != "verified" {
				failures = append(failures, fmt.Errorf("%s: %s is %s", entry.ID, scenario.Test, row.Status))
			}
		}
	}
	bindings, bindingsErr := runChatEvidenceTests(ctx, filepath.Join(root, "dev/providerconformance"), "^TestChatBindings$", "")
	if bindingsErr != nil {
		failures = append(failures, fmt.Errorf("public bindings: %w", bindingsErr))
	}
	for _, entry := range chatcheck.Plan() {
		row := bindings.Evidence(entry.ID, "offline_binding", "", chatcheck.Case{Test: "TestChatBindings/" + entry.ID, Semantics: "text_tool_round_trip_json_schema", Support: "supported"})
		row.Directory = "dev/providerconformance"
		evidence = append(evidence, row)
		if row.Status != "verified" {
			failures = append(failures, fmt.Errorf("%s public binding is %s", entry.ID, row.Status))
		}
	}
	results, liveErr := runChatEvidenceTests(ctx, filepath.Join(root, "dev/providerconformance"), "^TestChatLive$", live)
	if liveErr != nil {
		failures = append(failures, fmt.Errorf("live: %w", liveErr))
	}
	for _, entry := range chatcheck.Plan() {
		for _, scenario := range chatcheck.LiveCases(entry.ID) {
			row := results.Evidence(entry.ID, "live_model", os.Getenv(entry.ModelEnv), scenario)
			row.Directory = "dev/providerconformance"
			evidence = append(evidence, row)
			if row.Outcome == "not_run" || (selected[entry.ID] && row.Status != "verified") {
				failures = append(failures, fmt.Errorf("%s: %s is %s", entry.ID, scenario.Test, row.Status))
			}
		}
	}
	report := struct {
		Revision   string               `json:"revision"`
		Dirty      bool                 `json:"dirty"`
		Go         string               `json:"go"`
		RecordedAt time.Time            `json:"recorded_at"`
		Evidence   []chatcheck.Evidence `json:"evidence"`
	}{strings.TrimSpace(string(revision)), len(status) != 0, runtime.Version(), time.Now().UTC(), evidence}
	encoded, encodeErr := jsonv2.Marshal(report)
	if encodeErr != nil {
		return encodeErr
	}
	if writeErr := os.WriteFile(output, append(encoded, '\n'), 0o600); writeErr != nil {
		return writeErr
	}
	return errors.Join(failures...)
}

func runChatEvidenceTests(ctx context.Context, directory, pattern, live string) (chatcheck.Results, error) {
	timeout := 10 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if timeout <= 0 {
		return chatcheck.Results{}, ctx.Err()
	}
	command := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-tags=integration", "-timeout="+timeout.String(), "-run", pattern, ".")
	command.Dir = directory
	command.WaitDelay = 5 * time.Second
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if name == chatcheck.LiveSelectionEnv || strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "GOOGLE_") || strings.HasPrefix(name, "GEMINI_") {
			continue
		}
		command.Env = append(command.Env, variable)
	}
	command.Env = append(command.Env, chatcheck.LiveSelectionEnv+"="+live)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	testErr := command.Run()
	results, readErr := chatcheck.Read(&stdout)
	return results, errors.Join(testErr, readErr)
}

func TestChatEvidenceRespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results, err := runChatEvidenceTests(ctx, t.TempDir(), "^TestChatBindings$", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context cancellation", err)
	}
	row := results.Evidence("binding", "live_model", "test-model", chatcheck.Case{Test: "TestChatBindings", Support: "supported"})
	if row.Status != "unverified" || row.Outcome != "not_run" || row.Support != "unknown" {
		t.Fatalf("canceled evidence = %+v", row)
	}
}
