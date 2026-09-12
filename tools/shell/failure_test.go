package shell

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/tool"
)

type outputExecutor struct {
	output Output
	err    error
}

func (o outputExecutor) Run(context.Context, Input) (Output, error) { return o.output, o.err }

func TestToolPreservesFailedExecutionOutput(t *testing.T) {
	cause := errors.New("collection failed")
	executable := mustTool(t, outputExecutor{output: Output{
		Stdout: []byte("file written"), Stderr: []byte("partial stderr"), ExitCode: -1,
		Duration: time.Second, Killed: true,
	}, err: cause})
	_, err := invokeTestTool(t.Context(), executable, `{"command":"write"}`)
	failure, ok := errors.AsType[*tool.Failure](err)
	if !ok || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want Failure wrapping cause", err)
	}
	var response Response
	if err := json.Unmarshal(failure.Output().Details, &response); err != nil {
		t.Fatal(err)
	}
	want := Response{Stdout: "file written", Stderr: "partial stderr", ExitCode: -1, Duration: "1s", Killed: true}
	if response != want {
		t.Fatalf("response = %#v, want %#v", response, want)
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
