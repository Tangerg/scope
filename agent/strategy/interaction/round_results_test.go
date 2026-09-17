package interaction_test

import (
	"bytes"
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestSettledResultsReadsRecoveryFactsWithoutHistoryStorage(t *testing.T) {
	store := agenttest.NewMemoryTreeDurability()
	entered := make(chan struct{})
	var modelCalls atomic.Int32
	deployment := configuredInteraction(t, interaction.DefinitionConfig{
		Name: "results.snapshot", Description: "Read retained results.", MaxConcurrentToolCalls: 1,
	}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls.Add(1)
		return toolBatchResponse(
			chat.ToolCall{ID: "first", Name: "first", Arguments: `{}`},
			chat.ToolCall{ID: "pending", Name: "pending", Arguments: `{}`},
		), nil
	})}, interaction.ToolSetConfig{Tools: []tool.Tool{
		&callbackTool{name: "first", call: func(context.Context, string) (string, error) { return "confirmed", nil }},
		&callbackTool{name: "pending", call: func(ctx context.Context, _ string) (string, error) {
			close(entered)
			<-ctx.Done()
			return "", ctx.Err()
		}},
	}})
	engine := publicationEngine(t, deployment, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	terminal, err := root.Await(t.Context())
	if err != nil || terminal.Status() != agent.StatusCanceled {
		t.Fatalf("terminal=%s error=%v", terminal.Status(), err)
	}
	if joinErr := root.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	head, found, err := store.LoadTree(t.Context(), root.ID())
	if err != nil || !found {
		t.Fatalf("load head: found=%v error=%v", found, err)
	}
	rounds, err := interaction.SettledResults(head)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("rounds=%d error=%v", len(rounds), err)
	}
	round := rounds[0]
	entries := round.Entries()
	if round.Relation().ProcessID() != root.ID() || round.ModelCallSequence() != 1 || round.CallCount() != 2 || round.Complete() ||
		len(entries) != 1 || entries[0].ToolCallIndex != 0 || entries[0].Call.ID != "first" ||
		entries[0].Result.ID != "first" || entries[0].Disposition != interaction.ResultSucceeded || publicationText(entries[0]) != "confirmed" {
		t.Fatalf("incorrect sparse results: round=%+v entries=%+v", round, entries)
	}
	encoded := round.JSON()
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
	stored, found, err := store.LoadTree(t.Context(), root.ID())
	if err != nil || !found || stored.Digest() != head.Digest() || modelCalls.Load() != 1 {
		t.Fatalf("projection changed execution or stored head: found=%v error=%v model calls=%d", found, err, modelCalls.Load())
	}
}
