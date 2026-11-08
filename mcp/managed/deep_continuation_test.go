package managed_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/mcp/managed"
)

func TestManagedDeepFormCanResumeOverSDK(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	s := &service{store: run.NewMemoryStore(), server: sdk.NewServer(&sdk.Implementation{Name: "audit", Version: "1"}, nil)}
	s.runtime = must(run.New(ctx, run.Config{Build: "audit-token-capacity", Store: s.store, TreeLimits: run.TreeLimits{MaxDepth: 96}}))
	t.Cleanup(func() { _ = s.runtime.Close(context.Background()) })
	p := program(s.runtime.Compiler(), &s.starts)
	for depth := range 80 {
		stage := must(workflow.Call(workflow.CallConfig{ID: "child", Child: run.ChildBinding{Program: p.Erase()}}))
		name := fmt.Sprintf("audit.level.%d", depth)
		if depth == 79 {
			name = "test.booking"
		}
		p = must(workflow.Compile[request, decision](s.runtime.Compiler(), workflow.Config{Name: name, Description: "Forward to nested child", Stages: []workflow.Stage{stage}}))
	}
	s.cfg = managed.Config[request, decision]{
		Runtime: s.runtime, Program: p, ResumeKey: [32]byte{42}, TokenLifetime: time.Minute,
		Authorize: func(_ context.Context, req *sdk.CallToolRequest) (managed.Grant, error) {
			value, ok := s.grants.Load(req.Session)
			if !ok {
				return managed.Grant{}, fmt.Errorf("unauthorized")
			}
			return value.(managed.Grant), nil
		},
		Project: func(d decision) (tool.Output, error) { return tool.NewTextOutput(d.City), nil },
	}
	if err := managed.Register(s.server, s.cfg); err != nil {
		t.Fatal(err)
	}
	client := s.connect(t, "audit-session", true)
	result, err := call(ctx, client, "", nil, "audit-order")
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsInput() || len(result.RequestState) > 8<<10 {
		t.Fatalf("expected bounded pending token: %#v", result)
	}
	completed, err := call(ctx, client, result.RequestState, answer("accept", map[string]any{"city": "Paris"}), "audit-order")
	if err != nil || completed.NeedsInput() {
		t.Fatalf("deep continuation failed: %#v, %v", completed, err)
	}
}
