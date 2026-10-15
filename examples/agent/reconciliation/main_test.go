package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent"
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
missing: canceled unknown=1 writes=1 attempts=1 model_calls=1 result="" reason="remote receipt not found; outcome unresolved"
unavailable: canceled unknown=1 writes=1 attempts=1 model_calls=1 result="" reason="remote: receipt query unavailable"
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
		status       agent.Status
		unknown      int
		writes       int
		modelCalls   int32
		message      string
	}{
		{name: "confirmed", status: agent.StatusCompleted, writes: 1, modelCalls: 2, message: "stored:release note"},
		{name: "rejected", reject: true, status: agent.StatusCompleted, modelCalls: 2, message: "rejected: write denied"},
		{name: "missing", availability: queryMissing, status: agent.StatusCanceled, unknown: 1, writes: 1, modelCalls: 1},
		{name: "unavailable", availability: queryUnavailable, status: agent.StatusCanceled, unknown: 1, writes: 1, modelCalls: 1},
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
				if result.Result.Termination().Status() != scenario.status || result.Message != scenario.message || result.ModelCalls != scenario.modelCalls {
					t.Fatalf("termination=%s message=%q model calls=%d", result.Result.Termination().Status(), result.Message, result.ModelCalls)
				}
				wantRemote := ledgerReport{Attempts: 1, Queries: 1, Writes: scenario.writes}
				if current := remote.report(); current != wantRemote || result.Remote != wantRemote {
					t.Fatalf("remote evidence=%+v after cleanup=%+v", result.Remote, current)
				}
				if result.Before.RootID() != result.Head.RootID() || result.Before.IncarnationID() == result.Head.IncarnationID() {
					t.Fatal("replacement did not preserve the tree and activate a new writer")
				}
				if len(result.Before.ProcessSnapshots()) != 2 || len(result.Head.ProcessSnapshots()) != 2 {
					t.Fatal("handoff changed the Interaction and Tool child identities")
				}
				for _, snapshot := range result.Before.ProcessSnapshots() {
					if !slices.ContainsFunc(result.Head.ProcessSnapshots(), func(current agent.ProcessSnapshot) bool {
						return current.Relation() == snapshot.Relation() && current.DeploymentRef() == snapshot.DeploymentRef()
					}) {
						t.Fatalf("handoff lost Process %s or changed its binding", snapshot.Relation().ProcessID())
					}
					if snapshot.Relation().IsRoot() && snapshot.Status() != agent.StatusPaused {
						t.Fatalf("live parent was not durably paused before handoff: %s", snapshot.Status())
					}
				}
				original, err := unknownRequest(result.Before)
				if err != nil || original.ID() != result.Request.ID() {
					t.Fatalf("original Unknown=%s error=%v", original.ID(), err)
				}
				unknown := 0
				for _, snapshot := range result.Head.ProcessSnapshots() {
					unknown += len(snapshot.UnknownEffectIDs())
					if snapshot.Status() != scenario.status {
						t.Fatalf("stored Process %s status=%s", snapshot.Relation().ProcessID(), snapshot.Status())
					}
				}
				if unknown != scenario.unknown {
					t.Fatalf("retained Unknowns=%d, want %d", unknown, scenario.unknown)
				}
				if scenario.unknown != 0 {
					retained, retainedErr := unknownRequest(result.Head)
					if retainedErr != nil || retained.ID() != original.ID() || retained.Relation() != original.Relation() || !strings.Contains(result.Reason, "remote") {
						t.Fatalf("unresolved identity or diagnostic lost: request=%+v reason=%q error=%v", retained, result.Reason, retainedErr)
					}
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
		_, tools, err := newDeployment(&remoteLedger{}, &receiptModel{})
		if err != nil {
			t.Fatal(err)
		}
		for _, wrong := range []receipt{
			{EffectID: agent.EffectID{}, Note: note{Text: "release note"}, Accepted: true},
			{EffectID: result.Request.ID(), Note: note{Text: "another operation"}, Accepted: true},
		} {
			lookup := func(context.Context, agent.EffectID) (receipt, bool, error) { return wrong, true, nil }
			resolved, _, investigationErr := reconcile(ctx, lookup, tools, result.Request, nil)
			if investigationErr == nil || investigationErr.Error() != "example: remote receipt does not match the original write" || resolved {
				t.Fatalf("mismatched receipt accepted: resolved=%t error=%v", resolved, investigationErr)
			}
		}
	})
}
