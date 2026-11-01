package agent_test

import (
	startrand "crypto/rand"

	"github.com/Tangerg/scope/agent/run/runtest"

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

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
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
	var delegates []agent.Delegate
	for name, deployment := range map[string]run.Program[run.Payload, run.Payload]{"delegate": worker, "unavailable": unavailable} {
		delegate, err := agent.NewDelegate(agent.DelegateConfig{Name: name, Description: "Publish delegated outcomes.", Deployment: deployment, Budget: run.Budget{Steps: run.NewQuota(8), Effects: run.NewQuota(8), Signals: run.NewQuota(8)}})
		if err != nil {
			t.Fatal(err)
		}
		delegates = append(delegates, delegate)
	}
	partialOutput := tool.NewTextOutput("one external write completed\n" + strings.Repeat("界", 900))
	partial, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: errors.New("remaining writes failed"), Output: partialOutput})
	if err != nil {
		t.Fatal(err)
	}
	tools := []tool.Tool{
		&callbackTool{name: "success", call: func(context.Context, string) (string, error) { toolsExecuted.Add(1); return "ordinary output", nil }},
		&callbackTool{name: "partial", call: func(context.Context, string) (string, error) { toolsExecuted.Add(1); return "", partial }},
		&callbackTool{name: "denied", call: func(context.Context, string) (string, error) {
			failure, failureErr := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: tool.NewTextOutput("operation not permitted")})
			if failureErr != nil {
				return "", failureErr
			}
			return "", failure
		}},
	}
	calls := []chat.ToolCall{
		{ID: "ordinary", Name: "success", Arguments: `{}`},
		{ID: "start_failed", Name: "unavailable", Arguments: `{"value":"go"}`},
		{ID: "worker_failed", Name: "delegate", Arguments: `{"value":"fail"}`},
		{ID: "partial", Name: "partial", Arguments: `{}`},
		{ID: "denied", Name: "denied", Arguments: `{}`},
		{ID: "worker_succeeded", Name: "delegate", Arguments: `{"value":"go"}`},
	}
	wantDisposition := []agent.ResultDisposition{agent.ResultSucceeded, agent.ResultRejected, agent.ResultFailed, agent.ResultFailed, agent.ResultRejected, agent.ResultSucceeded}
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
			if index == 0 && !reflect.DeepEqual(entry.Result.Output, tool.NewTextOutput("ordinary output")) || index == 3 && !reflect.DeepEqual(entry.Result.Output, partialOutput) {
				return nil, errors.New("known executor output changed")
			}
			if entry.Call.ID == "worker_failed" {
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
	deployment := configuredInteraction(t, agent.DefinitionConfig{Name: "publication.mixed", Description: "Commit all known result producers.", MaxModelCalls: run.NewQuota(2), Delegates: delegates}, agent.DispatcherConfig{Model: model}, agent.ToolSetConfig{Tools: tools})
	engine, err := run.New(t.Context(), run.Config{
		Store: store,
		ProcessAdmitter: run.ProcessAdmitterFunc(func(_ context.Context, admission run.ProcessAdmission) error {
			if admission.ProgramRef().Name() == "publication.unavailable" {
				return errors.New("worker unavailable")
			}
			return nil
		}),
	})
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
	result, err := runtest.Observe(ctx, engine, deployment.Program, interactionInput(t, "work"))
	if err != nil || result.Termination().Status() != run.StatusCompleted || modelCalls != 2 {
		t.Fatalf("mixed publication result=%s err=%v model=%d", result.Termination().Status(), err, modelCalls)
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
	nested := configuredInteraction(t, agent.DefinitionConfig{Name: "publication.nested", Description: "Complete a nested Interaction."}, agent.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return toolCallResponse(chat.ToolCall{ID: "nested-tool", Name: "read", Arguments: `{}`}), nil
	})}, agent.ToolSetConfig{Tools: executables})
	sibling := configuredInteraction(t, agent.DefinitionConfig{Name: "publication.unresolved", Description: "Retain an unresolved external Tool."}, agent.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return toolCallResponse(chat.ToolCall{ID: "unknown-tool", Name: "blocked", Arguments: `{}`}), nil
	})}, agent.ToolSetConfig{Tools: executables})
	var delegates []agent.Delegate
	for name, child := range map[string]run.Program[run.Payload, run.Payload]{"known": nested.Program, "unknown": sibling.Program} {
		delegate, err := agent.NewDelegate(agent.DelegateConfig{Name: name, Description: "Delegate work.", Deployment: child})
		if err != nil {
			t.Fatal(err)
		}
		delegates = append(delegates, delegate)
	}
	input, err := run.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("nested"))}})
	if err != nil {
		t.Fatal(err)
	}
	var models atomic.Int32
	parent := configuredInteraction(t, agent.DefinitionConfig{Name: "publication.parent", Description: "Retain a known Delegate despite its sibling.", Delegates: delegates}, agent.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		models.Add(1)
		return publicationResponse(chat.ToolCall{ID: "known-child", Name: "known", Arguments: string(input.JSON())}, chat.ToolCall{ID: "unknown-child", Name: "unknown", Arguments: string(input.JSON())}), nil
	})}, agent.ToolSetConfig{})
	store.after = func(_ run.TreeSnapshot, publications []agent.RoundResults) error {
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
	root, err := engine.Start(ctx, parent.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "work")})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	<-known
	cancel()
	if cancelErr := root.Cancel(t.Context(), "end managed task"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	terminal, err := root.Await(t.Context())
	if err != nil || terminal.Termination().Status() != run.StatusCanceled {
		t.Fatalf("terminal=%s err=%v", terminal.Termination().Status(), err)
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
