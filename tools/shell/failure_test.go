package shell

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/content"
)

type outputExecutor struct {
	output Output
	err    error
}

func (o outputExecutor) Run(context.Context, Input) (Output, error) { return o.output, o.err }

func TestToolPreservesUnknownExecutionEvidence(t *testing.T) {
	cause := fmt.Errorf("collection failed: %w", context.DeadlineExceeded)
	executable := mustTool(t, outputExecutor{output: Output{
		Stdout: []byte("file written"), Stderr: []byte("partial stderr"), ExitCode: -1,
		Duration: time.Second, CancellationObserved: true, StdoutTruncated: true,
	}, err: cause})
	_, err := invokeTestTool(t.Context(), executable, `{"command":"write"}`)
	callErr, ok := errors.AsType[*tool.CallError](err)
	if !ok || !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want CallError retaining original cause", err)
	}
	if failure, found := errors.AsType[*tool.Failure](err); found {
		t.Fatalf("uncertain collection became a definite failure: %v", failure)
	}
	var response Response
	if err := jsonv2.Unmarshal(callErr.Evidence().Details, &response); err != nil {
		t.Fatal(err)
	}
	want := Response{Stdout: content.New([]byte("file written")), Stderr: content.New([]byte("partial stderr")), ExitCode: -1, Duration: "1s", CancellationObserved: true, StdoutTruncated: true}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response = %#v, want %#v", response, want)
	}
}

func TestToolPreservesBackendDefiniteOutcome(t *testing.T) {
	for _, kind := range []tool.FailureKind{tool.FailureKindFailed, tool.FailureKindRejected} {
		failure, err := tool.NewFailure(tool.FailureConfig{
			Kind: kind, Cause: context.DeadlineExceeded,
			Output: chat.NewTextToolOutput("backend's complete outcome"),
		})
		if err != nil {
			t.Fatal(err)
		}
		executable := mustTool(t, outputExecutor{err: errors.Join(ErrInvalidInput, failure)})
		output, err := invokeTestTool(t.Context(), executable, `{"command":"write"}`)
		got, found := errors.AsType[*tool.Failure](err)
		if !found || got != failure || got.Kind() != kind || !reflect.DeepEqual(got.Output(), failure.Output()) {
			t.Fatalf("definite outcome changed: %v", err)
		}
		if errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(output, chat.ToolOutput{}) {
			t.Fatalf("diagnostic cause or redundant output crossed complete outcome boundary: %+v, %v", output, err)
		}
	}
}

func TestToolPreservesBackendEvidenceOwner(t *testing.T) {
	want := chat.NewTextToolOutput("remote job 17 acknowledged, exit unobserved")
	callErr, err := tool.NewCallError(tool.CallErrorConfig{Cause: errors.Join(ErrInvalidInput, context.Canceled), Evidence: want})
	if err != nil {
		t.Fatal(err)
	}
	executable := mustTool(t, outputExecutor{err: callErr})
	_, err = invokeTestTool(t.Context(), executable, `{"command":"write"}`)
	got, found := errors.AsType[*tool.CallError](err)
	if !found || got != callErr || !reflect.DeepEqual(got.Evidence(), want) || !errors.Is(err, context.Canceled) {
		t.Fatalf("backend evidence was replaced with an empty response: %v", err)
	}
}

func TestToolNonzeroExitRemainsACompletedResult(t *testing.T) {
	executable := mustTool(t, outputExecutor{output: Output{ExitCode: 7, Stderr: []byte("test failure")}})
	output, err := invokeTestTool(t.Context(), executable, `{"command":"test"}`)
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := jsonv2.Unmarshal(output.Details, &response); err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 7 || string(response.Stderr.Bytes()) != "test failure" {
		t.Fatalf("completed command result = %+v", response)
	}
}

func TestLocalExecutorDoesNotInventExitCodeForSpawnFailure(t *testing.T) {
	skipWithoutShell(t)
	executor := mustLocalExecutor(t, LocalExecutorConfig{Directory: t.TempDir(), Shell: filepath.Join(t.TempDir(), "missing-shell")})
	output, err := executor.Run(t.Context(), Input{Cmd: "exit 0"})
	if !errors.Is(err, os.ErrNotExist) || output.ExitCode != -1 || output.CancellationObserved {
		t.Fatalf("spawn observation = %+v, %v", output, err)
	}
}

func TestLocalExecutorRecordsCancellationBeforeSpawn(t *testing.T) {
	skipWithoutShell(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	executor := mustLocalExecutor(t, LocalExecutorConfig{Directory: t.TempDir()})
	output, err := executor.Run(ctx, Input{Cmd: "exit 0"})
	if !errors.Is(err, context.Canceled) || output.ExitCode != -1 || !output.CancellationObserved {
		t.Fatalf("canceled spawn observation = %+v, %v", output, err)
	}
}

func TestLocalExecutorReportsActualStreamTruncation(t *testing.T) {
	skipWithoutShell(t)
	executor := mustLocalExecutor(t, LocalExecutorConfig{Directory: t.TempDir(), MaxBytesPerStream: 4})
	output, err := executor.Run(t.Context(), Input{Cmd: "printf abcde; printf xy >&2"})
	if err != nil {
		t.Fatal(err)
	}
	if !output.StdoutTruncated || output.StderrTruncated || string(output.Stderr) != "xy" {
		t.Fatalf("truncation observations = %+v", output)
	}
	executor = mustLocalExecutor(t, LocalExecutorConfig{Directory: t.TempDir(), MaxBytesPerStream: 100})
	output, err = executor.Run(t.Context(), Input{Cmd: "printf '... [1 bytes truncated] ...'"})
	if err != nil {
		t.Fatal(err)
	}
	if output.StdoutTruncated || output.StderrTruncated {
		t.Fatalf("command text was mistaken for truncation: %+v", output)
	}
}

func TestToolPreservesNonUTF8Evidence(t *testing.T) {
	for _, cause := range []error{nil, context.DeadlineExceeded} {
		executable := mustTool(t, outputExecutor{output: Output{
			Stdout: []byte{0xe4, 0xbd}, Stderr: []byte{0xff}, ExitCode: -1,
			StdoutTruncated: true, CancellationObserved: true,
		}, err: cause})
		output, err := invokeTestTool(t.Context(), executable, `{"command":"binary"}`)
		if cause != nil {
			callErr, found := errors.AsType[*tool.CallError](err)
			if !found || !errors.Is(err, cause) {
				t.Fatalf("non-UTF8 stdout erased execution evidence: %v", err)
			}
			output = callErr.Evidence()
		} else if err != nil {
			t.Fatalf("completed binary command became an error: %v", err)
		}
		var response Response
		if err := jsonv2.Unmarshal(output.Details, &response); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(response.Stdout.Bytes(), []byte{0xe4, 0xbd}) || !bytes.Equal(response.Stderr.Bytes(), []byte{0xff}) || response.ExitCode != -1 || !response.StdoutTruncated || !response.CancellationObserved {
			t.Fatalf("binary execution observations changed: %+v", response)
		}
	}
}

func TestToolKeepsBytesWhenLocalCaptureSplitsUTF8(t *testing.T) {
	skipWithoutShell(t)
	executable := mustTool(t, mustLocalExecutor(t, LocalExecutorConfig{Directory: t.TempDir(), MaxBytesPerStream: 2}))
	output, err := invokeTestTool(t.Context(), executable, `{"command":"printf '\\344\\275\\240'"}`)
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := jsonv2.Unmarshal(output.Details, &response); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xe4, 0xbd}, []byte("... [1 bytes truncated] ...\n")...)
	if !response.StdoutTruncated || !bytes.Equal(response.Stdout.Bytes(), want) {
		t.Fatalf("truncated UTF8 output = %x, want %x", response.Stdout.Bytes(), want)
	}
}

func TestToolDescriptionUsesHostPolicy(t *testing.T) {
	executable := mustTool(t, outputExecutor{})
	definition := executable.Definition()
	for _, absent := range []string{"/bin/sh", "`glob`", "`read`", "fresh shell"} {
		if strings.Contains(definition.Description, absent) {
			t.Fatalf("description contains %q", absent)
		}
	}
	if strings.Contains(string(definition.InputSchema), "/bin/sh") {
		t.Fatal("schema assumes a shell")
	}
	configured, err := NewTool(Config{Executor: outputExecutor{}, Description: "Run commands remotely using PowerShell."})
	if err != nil {
		t.Fatal(err)
	}
	if configured.Definition().Description != "Run commands remotely using PowerShell." {
		t.Fatal("host description changed")
	}
}
