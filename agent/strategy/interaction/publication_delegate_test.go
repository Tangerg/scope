package interaction_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestRoundResultsCoversMixedToolAndDelegatePaths(t *testing.T) {
	var toolsExecuted, workersExecuted atomic.Int32
	worker := delegateWorkflow(t, "publication.worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
		workersExecuted.Add(1)
		if input.Value == "fail" {
			return delegateResponse{}, errors.New(strings.Repeat("worker diagnostic 界", 300))
		}
		return delegateResponse{Value: "worker output"}, nil
	})
	unavailable := delegateWorkflow(t, "publication.unavailable", func(context.Context, delegateRequest) (delegateResponse, error) {
		t.Error("unavailable worker executed")
		return delegateResponse{}, nil
	})
	var delegates []interaction.Delegate
	for name, deployment := range map[string]agent.Deployment{"delegate": worker, "unavailable": unavailable} {
		delegate, err := interaction.NewDelegate(interaction.DelegateConfig{Name: name, Description: "Publish delegated outcomes.", Deployment: deployment, Budget: agent.Budget{Steps: agent.NewQuota(8), Effects: agent.NewQuota(8), Signals: agent.NewQuota(8)}})
		if err != nil {
			t.Fatal(err)
		}
		delegates = append(delegates, delegate)
	}
	partialOutput := chat.NewTextToolOutput("one external write completed\n" + strings.Repeat("界", 900))
	partial, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: errors.New("remaining writes failed"), Output: partialOutput})
	if err != nil {
		t.Fatal(err)
	}
	tools := []tool.Tool{
		&callbackTool{name: "success", call: func(context.Context, string) (string, error) { toolsExecuted.Add(1); return "ordinary output", nil }},
		&callbackTool{name: "partial", call: func(context.Context, string) (string, error) { toolsExecuted.Add(1); return "", partial }},
		&callbackTool{name: "denied", call: func(context.Context, string) (string, error) {
			failure, failureErr := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: chat.NewTextToolOutput("operation not permitted")})
			if failureErr != nil {
				return "", failureErr
			}
			return "", failure
		}},
	}
	calls := []chat.ToolCall{
		{ID: "ordinary", Name: "success", Arguments: `{}`},
		{ID: "missing", Name: "unknown", Arguments: `{}`},
		{ID: "bad_json", Name: "delegate", Arguments: `{"value":`},
		{ID: "bad_schema", Name: "delegate", Arguments: `{"value":true}`},
		{ID: "start_failed", Name: "unavailable", Arguments: `{"value":"go"}`},
		{ID: "worker_failed", Name: "delegate", Arguments: `{"value":"fail"}`},
		{ID: "partial", Name: "partial", Arguments: `{}`},
		{ID: "denied", Name: "denied", Arguments: `{}`},
		{ID: "worker_succeeded", Name: "delegate", Arguments: `{"value":"go"}`},
	}
	wantDisposition := []interaction.ResultDisposition{interaction.ResultSucceeded, interaction.ResultRejected, interaction.ResultRejected, interaction.ResultRejected, interaction.ResultRejected, interaction.ResultFailed, interaction.ResultFailed, interaction.ResultRejected, interaction.ResultSucceeded}
	store := newPublicationStore(t)
	modelCalls := 0
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		modelCalls++
		if modelCalls == 1 {
			parts := make([]chat.Part, len(calls))
			for index, call := range calls {
				parts[index] = chat.NewToolCallPart(call)
			}
			message := chat.NewAssistantMessage(parts...)
			return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, nil
		}
		entries := store.entries()
		if len(entries) != len(calls) || toolsExecuted.Load() != 2 || workersExecuted.Load() != 2 {
			return nil, errors.New("model advanced before all known results were durable")
		}
		committed := make([]chat.ToolResult, len(entries))
		for index, entry := range entries {
			if entry.Call != calls[index] || entry.ToolCallIndex != uint32(index) || entry.Disposition != wantDisposition[index] {
				return nil, fmt.Errorf("wrong result attribution: %d", index)
			}
			if index == 0 && !reflect.DeepEqual(entry.Result.Output, chat.NewTextToolOutput("ordinary output")) || index == 6 && !reflect.DeepEqual(entry.Result.Output, partialOutput) {
				return nil, errors.New("known executor output changed")
			}
			if index == 5 {
				text, _ := entry.Result.Output.Text()
				if len(text) < 2048 || !utf8.ValidString(text) {
					return nil, errors.New("invalid long worker diagnostic")
				}
			}
			committed[index] = entry.Result
		}
		parts := request.Messages[len(request.Messages)-1].Parts
		if len(parts) != len(calls) {
			return nil, errors.New("round was fragmented")
		}
		for index, part := range parts {
			if part.ToolResult == nil || !reflect.DeepEqual(*part.ToolResult, committed[index]) {
				return nil, fmt.Errorf("model result changed: %d", index)
			}
		}
		return textResponse("done"), nil
	})
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.mixed", Description: "Commit all known result producers.", MaxModelCalls: agent.NewQuota(2), Delegates: delegates}, interaction.DispatcherConfig{Model: model}, interaction.ToolSetConfig{Tools: tools})
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolveWith(worker), TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := engine.Run(ctx, deployment.Deployment, interactionInput(t, "work"))
	if err != nil || result.Status() != agent.StatusCompleted || modelCalls != 2 {
		t.Fatalf("mixed publication result=%s err=%v model=%d", result.Status(), err, modelCalls)
	}
}

func TestDelegatePublicationSurvivesUnresolvedSibling(t *testing.T) {
	store := newPublicationStore(t)
	entered, release, known := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, knownOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	executables := []tool.Tool{
		directTool{Tool: &callbackTool{name: "read", call: func(context.Context, string) (string, error) { return "nested confirmed", nil }}},
		&callbackTool{name: "blocked", call: func(context.Context, string) (string, error) {
			close(entered)
			<-release
			return "", errors.New("unknown external outcome")
		}},
	}
	nested := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.nested", Description: "Complete a nested Interaction."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return toolCallResponse(chat.ToolCall{ID: "nested-tool", Name: "read", Arguments: `{}`}), nil
	})}, interaction.ToolSetConfig{Tools: executables})
	sibling := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.unresolved", Description: "Retain an unresolved external Tool."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return toolCallResponse(chat.ToolCall{ID: "unknown-tool", Name: "blocked", Arguments: `{}`}), nil
	})}, interaction.ToolSetConfig{Tools: executables})
	var delegates []interaction.Delegate
	for name, child := range map[string]agent.Deployment{"known": nested.Deployment, "unknown": sibling.Deployment} {
		delegate, err := interaction.NewDelegate(interaction.DelegateConfig{Name: name, Description: "Delegate work.", Deployment: child})
		if err != nil {
			t.Fatal(err)
		}
		delegates = append(delegates, delegate)
	}
	input, err := agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("nested"))}})
	if err != nil {
		t.Fatal(err)
	}
	var models atomic.Int32
	parent := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.parent", Description: "Retain a known Delegate despite its sibling.", Delegates: delegates}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		models.Add(1)
		return publicationResponse(chat.ToolCall{ID: "known-child", Name: "known", Arguments: string(input.JSON())}, chat.ToolCall{ID: "unknown-child", Name: "unknown", Arguments: string(input.JSON())}), nil
	})}, interaction.ToolSetConfig{})
	parent.resolver = nested.resolveWith(nested.Deployment, sibling.Deployment)
	for ref, deployment := range sibling.resolver {
		parent.resolver[ref] = deployment
	}
	store.after = func(_ agent.TreeSnapshot, publications []interaction.RoundResults) error {
		for _, publication := range publications {
			if publication.Relation().IsRoot() && len(publication.Entries()) == 1 && publication.Entries()[0].Call.ID == "known-child" {
				knownOnce.Do(func() { close(known) })
			}
		}
		return nil
	}
	engine := publicationEngine(t, parent, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root, err := engine.Start(ctx, parent.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	<-known
	cancel()
	terminal, err := root.Await(t.Context())
	if err != nil || terminal.Status() != agent.StatusCanceled {
		t.Fatalf("terminal=%s err=%v", terminal.Status(), err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := root.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries := store.entries()
	if len(entries) != 2 || entries[0].Call.ID != "nested-tool" || entries[1].Call.ID != "known-child" || models.Load() != 1 {
		t.Fatalf("projection order=%+v models=%d", entries, models.Load())
	}
	unknown := 0
	for _, process := range store.tree().ProcessSnapshots() {
		unknown += len(process.UnknownEffectIDs())
	}
	if unknown != 1 {
		t.Fatalf("unresolved effects=%d", unknown)
	}
}
