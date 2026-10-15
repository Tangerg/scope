//go:build unix

package main

import (
	"bufio"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/tools/process"
)

const (
	expertStatusURI       = "expert://status"
	expertStarted         = "search:started"
	expertCanceled        = "search:canceled"
	expertDisconnectQuery = "disconnect"
	expertWaitQuery       = "wait"
	expertExitCode        = 23
	expertSourceURL       = "https://example.invalid/independent-expert"
)

type expertStatus struct {
	PID      int   `json:"pid"`
	Calls    int64 `json:"calls"`
	Canceled int64 `json:"canceled"`
}

type expertArtifact struct {
	binary string
	digest agent.Digest
}

type stdioPeer struct {
	artifact expertArtifact
	session  *sdkmcp.ClientSession
	command  *process.Process
	events   chan string
	readDone chan struct{}
	readErr  error
}

func buildExpert(t *testing.T) expertArtifact {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "expert")
	command := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../expert")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build independent expert: %v\n%s", err, output)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return expertArtifact{binary: binary, digest: agent.ComputeDigest(data)}
}

func openExpert(t *testing.T, ctx context.Context, artifact expertArtifact) *stdioPeer {
	t.Helper()
	p := &stdioPeer{artifact: artifact, events: make(chan string, 8)}
	var pipes []*os.File
	t.Cleanup(func() {
		if p.session != nil {
			if err := p.session.Close(); err != nil {
				t.Error(err)
			}
		}
		if p.command != nil {
			if err := p.command.Close(); err != nil {
				t.Error(err)
			}
		}
		for _, pipe := range pipes {
			if err := pipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Error(err)
			}
		}
		if p.readDone != nil {
			<-p.readDone
			if p.readErr != nil && !errors.Is(p.readErr, os.ErrClosed) {
				t.Error(p.readErr)
			}
		}
	})
	newPipe := func() (*os.File, *os.File) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		pipes = append(pipes, reader, writer)
		return reader, writer
	}
	input, requests := newPipe()
	responses, output := newPipe()
	diagnostics, stderr := newPipe()
	command, err := process.New(ctx, process.Config{
		Argv: []string{artifact.binary}, Directory: t.TempDir(), Env: []string{},
		Stdin: input, Stdout: output, Stderr: stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.command = command
	if startErr := p.command.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	for _, inherited := range []*os.File{input, output, stderr} {
		if closeErr := inherited.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	p.readDone = make(chan struct{})
	go p.readEvents(ctx, diagnostics)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "scope-process-consumer"}, nil)
	p.session, err = client.Connect(ctx, &sdkmcp.IOTransport{Reader: responses, Writer: requests}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := p.status(t, ctx)
	if status.PID == os.Getpid() || status.PID <= 0 || status.Calls != 0 {
		t.Fatalf("expert is not an independent fresh process: %+v", status)
	}
	return p
}

func (s *stdioPeer) readEvents(ctx context.Context, reader *os.File) {
	defer close(s.readDone)
	defer close(s.events)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		select {
		case s.events <- scanner.Text():
		case <-ctx.Done():
			return
		}
	}
	s.readErr = scanner.Err()
}

func (s *stdioPeer) await(t *testing.T, ctx context.Context, expected string) {
	t.Helper()
	select {
	case event, open := <-s.events:
		if !open || event != expected {
			t.Fatalf("expert event=%q open=%t, want %q", event, open, expected)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func (s *stdioPeer) status(t *testing.T, ctx context.Context) expertStatus {
	t.Helper()
	result, err := s.session.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: expertStatusURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) != 1 || result.Contents[0].URI != expertStatusURI {
		t.Fatalf("unexpected expert status: %+v", result)
	}
	var status expertStatus
	if err := jsonv2.Unmarshal([]byte(result.Contents[0].Text), &status); err != nil {
		t.Fatal(fmt.Errorf("decode expert status: %w", err))
	}
	return status
}
