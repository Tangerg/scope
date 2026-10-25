package interaction_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type failedAdvertisementTool struct {
	tool.Tool
	defect string
}

func (f failedAdvertisementTool) AdvertiseDeferred(context.Context, tool.Invocation, tool.Result) ([]string, error) {
	switch f.defect {
	case "error":
		return nil, errors.New("advertisement failed after the write")
	case "panic":
		panic("advertisement failed after the write")
	default:
		return []string{"unbound"}, nil
	}
}

func TestAdvertisementFailureRetainsCompletedResultInFinalRecoveryCut(t *testing.T) {
	for _, defect := range []string{"error", "panic", "unbound"} {
		t.Run(defect, func(t *testing.T) {
			var writes, modelCalls atomic.Int32
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "write", Description: "Write one result."}, func(context.Context, struct{}) (string, error) {
				writes.Add(1)
				return "completed write", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			call := chat.ToolCall{ID: "write-1", Name: "write", Arguments: `{}`}
			deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "results.advertisement", Description: "Retain failed postprocessing evidence."}, interaction.DispatcherConfig{
				Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					modelCalls.Add(1)
					return toolBatchResponse(call), nil
				}),
			}, interaction.ToolSetConfig{Tools: []tool.Tool{failedAdvertisementTool{Tool: executable, defect: defect}}})
			store := agent.NewMemoryTreeCommitter()
			engine := publicationEngine(t, deployment, store)
			result, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "write"))
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != agent.FailureKindContract || failure.Code() != "interaction.tool.advertisement_invalid" {
				t.Fatalf("postprocessing failure=%v", result.Termination())
			}
			head, found, err := store.LoadTree(t.Context(), result.ProcessID())
			if err != nil || !found {
				t.Fatalf("load final head: found=%v error=%v", found, err)
			}
			assertResult := func(snapshot agent.TreeSnapshot) interaction.RoundResults {
				t.Helper()
				for _, process := range snapshot.ProcessSnapshots() {
					if len(process.UnknownEffectIDs()) != 0 {
						t.Fatal("known completed write became unknown")
					}
				}
				rounds, projectionErr := interaction.SettledResults(snapshot)
				if projectionErr != nil || len(rounds) != 1 {
					t.Fatalf("completed write missing: rounds=%d error=%v", len(rounds), projectionErr)
				}
				round := rounds[0]
				entries := round.Entries()
				reference, present := round.Reference(0)
				if !round.Complete() || round.CallCount() != 1 || len(entries) != 1 || entries[0].Call != call ||
					entries[0].Disposition != interaction.ResultSucceeded || publicationText(entries[0]) != "completed write" ||
					!present || reference.ProcessID() != result.ProcessID() || reference.ModelCallSequence() != 1 || reference.ToolCallIndex() != 0 {
					t.Fatalf("completed write changed: round=%+v entries=%+v reference=%v", round, entries, reference)
				}
				return round
			}
			original := assertResult(head)
			head, err = agent.ParseTreeSnapshot(head.JSON())
			if err != nil {
				t.Fatal(err)
			}
			assertResult(head)
			recovered := publicationEngine(t, deployment, store)
			process, err := recovered.RestoreTree(t.Context(), deployment.Deployment, head)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := process.Await(t.Context())
			if err != nil || restored.Termination().Status() != agent.StatusFailed {
				t.Fatalf("restored termination=%v error=%v", restored.Termination(), err)
			}
			head, err = recovered.CaptureTree(t.Context(), result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			if round := assertResult(head); round.Digest() != original.Digest() || !bytes.Equal(round.JSON(), original.JSON()) {
				t.Fatal("restore changed exact completed result or attribution")
			}
			if writes.Load() != 1 || modelCalls.Load() != 1 {
				t.Fatalf("failed postprocessing replayed work: writes=%d model calls=%d", writes.Load(), modelCalls.Load())
			}
		})
	}
}

func TestSettledResultsReadsRecoveryFactsWithoutHistoryStorage(t *testing.T) {
	store := agent.NewMemoryTreeCommitter()
	entered := make(chan struct{}, 2)
	var modelCalls atomic.Int32
	var unscheduledCalls atomic.Int32
	deployment := configuredInteraction(t, interaction.DefinitionConfig{
		Name: "results.snapshot", Description: "Read retained results.", MaxConcurrentToolCalls: 2,
	}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls.Add(1)
		return toolBatchResponse(
			chat.ToolCall{ID: "first", Name: "first", Arguments: `{}`},
			chat.ToolCall{ID: "pending", Name: "pending", Arguments: `{}`},
			chat.ToolCall{ID: "unscheduled", Name: "unscheduled", Arguments: `{}`},
		), nil
	})}, interaction.ToolSetConfig{Tools: []tool.Tool{
		publicationConcurrentTool{&callbackTool{name: "first", call: func(ctx context.Context, _ string) (string, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return "confirmed", nil
		}}},
		publicationConcurrentTool{&callbackTool{name: "pending", call: func(ctx context.Context, _ string) (string, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		}}},
		publicationConcurrentTool{&callbackTool{name: "unscheduled", call: func(context.Context, string) (string, error) {
			unscheduledCalls.Add(1)
			return "unexpected", nil
		}}},
	}})
	engine := publicationEngine(t, deployment, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	<-entered
	cancel()
	terminal, err := root.Await(t.Context())
	if err != nil || terminal.Termination().Status() != agent.StatusCanceled {
		t.Fatalf("terminal=%s error=%v", terminal.Termination().Status(), err)
	}
	if joinErr := root.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	head, found, err := store.LoadTree(t.Context(), root.Relation().ProcessID())
	if err != nil || !found {
		t.Fatalf("load head: found=%v error=%v", found, err)
	}
	rounds, err := interaction.SettledResults(head)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("rounds=%d error=%v", len(rounds), err)
	}
	round := rounds[0]
	entries := round.Entries()
	if round.Relation().ProcessID() != root.Relation().ProcessID() || round.ModelCallSequence() != 1 || round.CallCount() != 3 || round.Complete() ||
		len(entries) != 1 || entries[0].ToolCallIndex != 0 || entries[0].Call.ID != "first" ||
		entries[0].Result.ID != "first" || entries[0].Disposition != interaction.ResultSucceeded || publicationText(entries[0]) != "confirmed" {
		t.Fatalf("incorrect sparse results: round=%+v entries=%+v", round, entries)
	}
	encoded := round.JSON()
	for index := range uint32(3) {
		reference, present := round.Reference(index)
		if !present || reference.ProcessID() != root.Relation().ProcessID() || reference.ModelCallSequence() != 1 || reference.ToolCallIndex() != index {
			t.Fatalf("sparse reference %d = %v, present=%v", index, reference, present)
		}
	}
	if round.Digest() != agent.ComputeDigest(encoded) {
		t.Fatal("content digest does not bind exact JSON")
	}
	entries[0].Call.Name = "changed"
	entries[0].Result.Output.Content[0].Text = "changed"
	encoded[0] = '!'
	again, err := interaction.SettledResults(head)
	if err != nil || len(again) != 1 || !reflect.DeepEqual(round.Entries(), again[0].Entries()) ||
		round.Digest() != again[0].Digest() || !bytes.Equal(round.JSON(), again[0].JSON()) {
		t.Fatalf("read projection is mutable or nondeterministic: %v", err)
	}
	stored, found, err := store.LoadTree(t.Context(), root.Relation().ProcessID())
	if err != nil || !found || stored.Digest() != head.Digest() || modelCalls.Load() != 1 || unscheduledCalls.Load() != 0 {
		t.Fatalf("projection changed execution or stored head: found=%v error=%v model calls=%d unscheduled calls=%d", found, err, modelCalls.Load(), unscheduledCalls.Load())
	}
}
