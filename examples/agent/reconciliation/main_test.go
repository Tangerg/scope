package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		var output bytes.Buffer
		if err := run(ctx, &output); err != nil {
			t.Fatal(err)
		}
		const want = `confirmed: completed unknown=0 writes=1 attempts=1 model_calls=2 result="stored:release note" reason=""
rejected: completed unknown=0 writes=0 attempts=1 model_calls=2 result="rejected: write denied" reason=""
missing: unresolved unknown=1 writes=1 attempts=1 model_calls=1 result="" reason="remote receipt not found; outcome unresolved"
unavailable: unresolved unknown=1 writes=1 attempts=1 model_calls=1 result="" reason="remote: receipt query unavailable"
`
		if output.String() != want {
			t.Fatalf("output=%q, want %q", output.String(), want)
		}
	})
}

func TestReconciliationPreservesExecutionAndBusinessFacts(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		reject       bool
		availability queryAvailability
		unknown      int
		writes       int
		modelCalls   int32
		message      string
	}{
		{name: "confirmed", writes: 1, modelCalls: 2, message: "stored:release note"},
		{name: "rejected", reject: true, modelCalls: 2, message: "rejected: write denied"},
		{name: "missing", availability: queryMissing, unknown: 1, writes: 1, modelCalls: 1},
		{name: "unavailable", availability: queryUnavailable, unknown: 1, writes: 1, modelCalls: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				remote := &remoteLedger{reject: scenario.reject, availability: scenario.availability}
				result, err := runScenario(ctx, remote)
				if err != nil {
					t.Fatal(err)
				}
				message, err := result.message()
				if err != nil || message != scenario.message || result.ModelCalls != scenario.modelCalls {
					t.Fatalf("message=%q model calls=%d error=%v", message, result.ModelCalls, err)
				}
				wantRemote := ledgerReport{Attempts: 1, Queries: 1, Writes: scenario.writes}
				if current := remote.report(); current != wantRemote || result.Remote != wantRemote {
					t.Fatalf("remote evidence=%+v after cleanup=%+v", result.Remote, current)
				}
				if result.Before.Root != result.Head.Root || result.Before.Epoch()+1 != result.Head.Epoch() {
					t.Fatal("replacement did not preserve the tree and advance its writer epoch")
				}
				before, err := result.Before.Inspect()
				if err != nil {
					t.Fatal(err)
				}
				head, err := result.Head.Inspect()
				if err != nil {
					t.Fatal(err)
				}
				if len(before.Processes) != 1 || len(head.Processes) != 1 {
					t.Fatal("ordinary tool call introduced another process lifecycle")
				}
				original, err := unknownRequest(result.Before)
				if err != nil || original.ID() != result.Request.ID() || before.Processes[0].Program != head.Processes[0].Program {
					t.Fatalf("original Unknown=%s error=%v", original.ID(), err)
				}
				if unknown := unknownCount(head); unknown != scenario.unknown {
					t.Fatalf("retained Unknowns=%d, want %d", unknown, scenario.unknown)
				}
				if scenario.unknown == 0 {
					if head.Processes[0].Status != agentrun.StatusCompleted {
						t.Fatal("resolved interaction did not complete")
					}
					return
				}
				retained, err := unknownRequest(result.Head)
				if err != nil || retained.ID() != original.ID() || !strings.Contains(result.Reason, "remote") {
					t.Fatalf("unresolved identity or diagnostic lost: request=%+v reason=%q error=%v", retained, result.Reason, err)
				}
				canceled := false
				for _, entry := range result.Head.Entries {
					switch record := entry.Record.(type) {
					case agentrun.ControlRequested:
						canceled = canceled || record.Kind == agentrun.ControlCancel
					case agentrun.Exited:
						t.Fatal("unresolved cancellation published a terminal fact")
					}
				}
				if !canceled || head.Processes[0].Status.Terminal() {
					t.Fatal("cancellation must remain pending until the uncertain effect is resolved")
				}
			})
		})
	}
}

func TestReceiptMustMatchOriginalWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		result, err := runScenario(ctx, &remoteLedger{availability: queryMissing})
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := agentrun.New(ctx, agentrun.Config{Build: exampleBuild, Store: agentrun.NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if closeErr := closeRuntime(ctx, runtime); closeErr != nil {
				t.Error(closeErr)
			}
		}()
		program, err := newProgram(runtime.Compiler(), &remoteLedger{}, &receiptModel{})
		if err != nil {
			t.Fatal(err)
		}
		invocation, err := interaction.ToolInvocationFromRequest(result.Request)
		if err != nil {
			t.Fatal(err)
		}
		for _, wrong := range []receipt{
			{IdempotencyKey: "another-call", Note: note{Text: "release note"}, Accepted: true},
			{IdempotencyKey: invocation.IdempotencyKey(), Note: note{Text: "another operation"}, Accepted: true},
		} {
			lookup := func(context.Context, string) (receipt, bool, error) { return wrong, true, nil }
			resolved, _, err := reconcile(ctx, lookup, program, result.Request, nil)
			if err == nil || err.Error() != "example: remote receipt does not match the original write" || resolved {
				t.Fatalf("mismatched receipt accepted: resolved=%t error=%v", resolved, err)
			}
		}
	})
}
