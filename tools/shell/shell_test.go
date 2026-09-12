package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func skipWithoutShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh available")
	}
}

func TestLocalExecutor_Run_HappyPath(t *testing.T) {
	skipWithoutShell(t)
	out, err := mustLocalExecutor(t, LocalConfig{Directory: "."}).Run(t.Context(), Input{Cmd: "echo hello"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(string(out.Stdout)); got != "hello" {
		t.Errorf("Stdout = %q, want %q", got, "hello")
	}
	if out.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.ExitCode)
	}
	if out.CancellationObserved {
		t.Errorf("CancellationObserved = true, want false")
	}
}

func TestLocalExecutor_Run_NonZeroExit(t *testing.T) {
	skipWithoutShell(t)
	out, err := mustLocalExecutor(t, LocalConfig{Directory: "."}).Run(t.Context(), Input{Cmd: "exit 7"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", out.ExitCode)
	}
}

func TestLocalExecutor_Run_StderrCaptured(t *testing.T) {
	skipWithoutShell(t)
	out, err := mustLocalExecutor(t, LocalConfig{Directory: "."}).Run(t.Context(), Input{Cmd: "echo oops 1>&2"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(string(out.Stderr)); got != "oops" {
		t.Errorf("Stderr = %q, want %q", got, "oops")
	}
}

func TestLocalExecutor_Run_Timeout(t *testing.T) {
	skipWithoutShell(t)
	start := time.Now()
	out, err := mustLocalExecutor(t, LocalConfig{Directory: "."}).Run(t.Context(), Input{
		Cmd:     "sleep 5",
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.CancellationObserved {
		t.Errorf("CancellationObserved = false, want true")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("elapsed %v > 2s — timeout didn't kick in", elapsed)
	}
}

func TestLocalExecutor_Run_Dir(t *testing.T) {
	skipWithoutShell(t)
	dir := t.TempDir()
	exec := mustLocalExecutor(t, LocalConfig{Directory: dir})
	out, err := exec.Run(t.Context(), Input{Cmd: "pwd"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// macOS symlinks /var → /private/var, so compare resolved suffixes.
	got := strings.TrimSpace(string(out.Stdout))
	if !strings.HasSuffix(got, strings.TrimPrefix(dir, "/private")) && got != dir {
		t.Errorf("pwd = %q, want it to run in Dir %q", got, dir)
	}
}

func TestNewLocalExecutorRejectsInvalidConfig(t *testing.T) {
	for name, config := range map[string]LocalConfig{
		"empty directory":  {},
		"blank shell":      {Directory: ".", Shell: " \t"},
		"negative maximum": {Directory: ".", MaxBytesPerStream: -1},
	} {
		if _, err := NewLocalExecutor(config); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s error = %v, want ErrInvalidConfig", name, err)
		}
	}
}

func TestLocalExecutor_Run_EmptyCommand(t *testing.T) {
	for _, command := range []string{"", " \t\n"} {
		_, err := mustLocalExecutor(t, LocalConfig{Directory: "."}).Run(t.Context(), Input{Cmd: command})
		if !errors.Is(err, ErrEmptyCommand) {
			t.Errorf("Run with empty Cmd: err = %v, want ErrEmptyCommand", err)
		}
	}
}

func TestLocalExecutorRejectsInvalidInput(t *testing.T) {
	if _, err := mustLocalExecutor(t, LocalConfig{Directory: "."}).Run(t.Context(), Input{Cmd: "true", Timeout: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative timeout error = %v, want ErrInvalidInput", err)
	}
	var nilExecutor *LocalExecutor
	if _, err := nilExecutor.Run(t.Context(), Input{Cmd: "true"}); !errors.Is(err, ErrNilExecutor) {
		t.Fatalf("nil executor error = %v, want ErrNilExecutor", err)
	}
	if _, err := new(LocalExecutor).Run(t.Context(), Input{Cmd: "true"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("zero executor error = %v, want ErrInvalidConfig", err)
	}
}

func TestLocalExecutor_OutputCap(t *testing.T) {
	skipWithoutShell(t)
	exec := mustLocalExecutor(t, LocalConfig{Directory: ".", MaxBytesPerStream: 100})
	out, err := exec.Run(t.Context(), Input{
		Cmd: `for i in $(seq 1 1000); do echo "line $i"; done`,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Stdout) > 200 {
		t.Errorf("Stdout length %d, expected ~100 + small marker", len(out.Stdout))
	}
	if !bytes.Contains(out.Stdout, []byte("truncated")) {
		t.Errorf("Stdout missing truncation marker: %q", out.Stdout)
	}
}

func TestTool_Definition(t *testing.T) {
	def := mustTool(t, mustLocalExecutor(t, LocalConfig{Directory: "."})).Definition()
	if def.Name != "shell" {
		t.Errorf("Name = %q, want %q", def.Name, "shell")
	}
	if len(def.InputSchema) == 0 {
		t.Error("InputSchema is empty")
	}
	if !strings.Contains(def.Description, "shell") {
		t.Errorf("Description missing 'shell': %q", def.Description)
	}
}

func TestTool_Call_HappyPath(t *testing.T) {
	skipWithoutShell(t)
	tool := mustTool(t, mustLocalExecutor(t, LocalConfig{Directory: "."}))
	result, err := invokeTestTool(t.Context(), tool, `{"command":"echo hi"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(result.Details, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v\nbody=%s", err, result.Details)
	}
	if !strings.Contains(resp.Stdout, "hi") {
		t.Errorf("Response.Stdout = %q, want substring %q", resp.Stdout, "hi")
	}
	if resp.ExitCode != 0 {
		t.Errorf("Response.ExitCode = %d, want 0", resp.ExitCode)
	}
}

func TestTool_Call_BadJSON(t *testing.T) {
	if _, err := invokeTestTool(t.Context(), mustTool(t, mustLocalExecutor(t, LocalConfig{Directory: "."})), `{bad json}`); err == nil {
		t.Fatal("Call with bad JSON: want error")
	}
}

func TestTool_Call_EnforcesPreciseInputContract(t *testing.T) {
	tool := mustTool(t, mustLocalExecutor(t, LocalConfig{Directory: "."}))
	for _, arguments := range []string{
		`{"command":"true","timeout":10}`,
		`{"command":"true","timeout_ms":600001}`,
		`{"command":"true","unknown":true}`,
		`{"command":"true"} {}`,
		`{}`,
	} {
		if _, err := invokeTestTool(t.Context(), tool, arguments); err == nil {
			t.Fatalf("shell accepted arguments outside its contract: %s", arguments)
		}
	}
}

func TestToolRejectsNilExecutor(t *testing.T) {
	if _, err := NewTool(Config{}); !errors.Is(err, ErrNilExecutor) {
		t.Fatalf("NewTool(Config{}) error = %v, want ErrNilExecutor", err)
	}
}

func TestToolRejectsTypedNilExecutor(t *testing.T) {
	var executor *LocalExecutor
	if _, err := NewTool(Config{Executor: executor}); !errors.Is(err, ErrNilExecutor) {
		t.Fatalf("NewTool(typed nil) error = %v, want ErrNilExecutor", err)
	}
}

func TestBoundedBuffer_Truncates(t *testing.T) {
	b := newBoundedBuffer(10)
	payload := []byte("hello world this is more than 10 bytes")
	n, err := b.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write returned %d, want %d (must pretend full write to keep pipe alive)", n, len(payload))
	}
	out := b.finalize()
	if !bytes.Contains(out, []byte("truncated")) {
		t.Errorf("finalize() missing marker: %q", out)
	}
	if b.dropped == 0 {
		t.Error("dropped = 0, want > 0")
	}
}

func TestBoundedBuffer_NoTruncation(t *testing.T) {
	b := newBoundedBuffer(100)
	if _, err := b.Write([]byte("short")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := b.finalize()
	if string(out) != "short" {
		t.Errorf("finalize() = %q, want %q", out, "short")
	}
	if bytes.Contains(out, []byte("truncated")) {
		t.Error("finalize() unexpectedly contains truncation marker")
	}
}
