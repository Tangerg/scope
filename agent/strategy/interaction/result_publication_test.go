package interaction_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type publicationConcurrentTool struct{ tool.Tool }

func (p publicationConcurrentTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) {
	return func(tool.Invocation) (string, bool) { return "", true }
}

func publicationResponse(calls ...chat.ToolCall) *chat.Response {
	parts := make([]chat.Part, len(calls))
	for index, call := range calls {
		parts[index] = chat.NewToolCallPart(call)
	}
	message := chat.NewAssistantMessage(parts...)
	return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}
}

func TestCancellationPreservesDefiniteResults(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, lateDefinite := range []bool{false, true} {
			t.Run(fmt.Sprintf("parallel=%v/late_definite=%v", parallel, lateDefinite), func(t *testing.T) {
				store := newPublicationStore(t)
				entered := make(chan agent.EffectID)
				release := make(chan struct{})
				var releaseOnce sync.Once
				defer releaseOnce.Do(func() { close(release) })
				known := make(chan struct{})
				var knownOnce sync.Once
				var modelCalls atomic.Int32
				var executed [3]atomic.Int32
				var executables []tool.Tool
				for index, name := range []string{"a", "b", "c"} {
					executable := &callbackTool{name: name, call: func(ctx context.Context, _ string) (string, error) {
						executed[index].Add(1)
						if index == 1 {
							invocation, ok := interaction.ToolInvocationFromContext(ctx)
							if !ok {
								return "", errors.New("missing tool invocation")
							}
							entered <- invocation.EffectID()
							<-release
							if lateDefinite {
								return "late confirmed", nil
							}
							return "", errors.New("external outcome cannot be confirmed")
						}
						return "confirmed " + name, nil
					}}
					if parallel {
						executables = append(executables, publicationConcurrentTool{executable})
					} else {
						executables = append(executables, executable)
					}
				}
				store.after = func(_ agent.TreeSnapshot, publications []interaction.RoundResults) error {
					for _, publication := range publications {
						entries := publication.Entries()
						if parallel && len(entries) == 2 && entries[0].ToolCallIndex == 0 && entries[1].ToolCallIndex == 2 {
							if publication.Complete() {
								return errors.New("sparse publication claimed completion")
							}
							knownOnce.Do(func() { close(known) })
						}
					}
					return nil
				}
				concurrency := 1
				if parallel {
					concurrency = 3
				}
				deployment := configuredInteraction(t, interaction.DefinitionConfig{Description: "Persist known results.", Name: "publication.cancel", MaxConcurrentToolCalls: concurrency}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					modelCalls.Add(1)
					return publicationResponse(chat.ToolCall{ID: "a", Name: "a", Arguments: `{}`}, chat.ToolCall{ID: "b", Name: "b", Arguments: `{}`}, chat.ToolCall{ID: "c", Name: "c", Arguments: `{}`}), nil
				})}, interaction.ToolSetConfig{Tools: executables})
				engine := publicationEngine(t, deployment, store)
				runContext, cancel := context.WithCancel(t.Context())
				defer cancel()
				root, err := engine.Start(runContext, deployment.Deployment, interactionInput(t, "work"))
				if err != nil {
					t.Fatal(err)
				}
				blockedEffect := <-entered
				if parallel {
					<-known
				}
				cancel()
				terminal, err := root.Await(t.Context())
				if err != nil || terminal.Status() != agent.StatusCanceled {
					t.Fatalf("terminal=%v err=%v", terminal.Status(), err)
				}
				releaseOnce.Do(func() { close(release) })
				if joinErr := root.Join(t.Context()); joinErr != nil {
					t.Fatal(joinErr)
				}
				again, err := root.Await(t.Context())
				if err != nil || !reflect.DeepEqual(terminal, again) {
					t.Fatalf("immutable terminal changed: %v", err)
				}
				path := store.path
				store.Close()
				reopened := openPublicationStore(t, path)
				entries := reopened.entries()
				slices.SortFunc(entries, func(a, b interaction.ResultEntry) int { return int(a.ToolCallIndex) - int(b.ToolCallIndex) })
				want := []uint32{0}
				if lateDefinite {
					want = append(want, 1)
				}
				if parallel {
					want = append(want, 2)
				}
				got := make([]uint32, len(entries))
				for index, entry := range entries {
					got[index] = entry.ToolCallIndex
					if entry.Disposition != interaction.ResultSucceeded || entry.Call.ID != entry.Result.ID {
						t.Fatalf("wrong attribution: %+v", entry)
					}
				}
				if !slices.Equal(got, want) || publicationText(entries[0]) != "confirmed a" {
					t.Fatalf("known indexes=%v want=%v entries=%+v", got, want, entries)
				}
				if executed[0].Load() != 1 || executed[1].Load() != 1 || executed[2].Load() != int32(concurrency/3) || modelCalls.Load() != 1 {
					t.Fatalf("execution counts=%d/%d/%d model=%d", executed[0].Load(), executed[1].Load(), executed[2].Load(), modelCalls.Load())
				}
				unresolved := 0
				for _, process := range reopened.tree().ProcessSnapshots() {
					unresolved += len(process.UnknownEffectIDs())
					for _, id := range process.UnknownEffectIDs() {
						if id != blockedEffect {
							t.Fatalf("unexpected unresolved effect %s", id)
						}
					}
					if process.ProcessID() == root.ID() {
						result, present := process.Result()
						if !present || !reflect.DeepEqual(result, terminal) {
							t.Fatal("reopened root terminal changed")
						}
					}
					if !process.Status().Terminal() {
						t.Fatalf("undrained process: %s", process.ProcessID())
					}
				}
				expectedUnknown := 1
				if lateDefinite {
					expectedUnknown = 0
				}
				if unresolved != expectedUnknown {
					t.Fatalf("unresolved=%d want=%d", unresolved, expectedUnknown)
				}
			})
		}
	}
}

func TestPublicationLostAcknowledgmentRecoversWithoutToolReplay(t *testing.T) {
	store := newPublicationStore(t)
	var executions, modelCalls atomic.Int32
	var lost atomic.Bool
	loss := errors.New("stored receipt acknowledgment lost")
	store.after = func(_ agent.TreeSnapshot, publications []interaction.RoundResults) error {
		if len(publications) > 0 && lost.CompareAndSwap(false, true) {
			return loss
		}
		return nil
	}
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Description: "Persist known results.", Name: "publication.recover"}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		if modelCalls.Add(1) == 1 {
			return toolCallResponse(chat.ToolCall{ID: "one", Name: "write", Arguments: `{}`}), nil
		}
		if len(store.entries()) != 1 {
			return nil, errors.New("continued without durable result")
		}
		return textResponse("done"), nil
	})}, interaction.ToolSetConfig{Tools: []tool.Tool{&callbackTool{name: "write", call: func(context.Context, string) (string, error) { executions.Add(1); return "exact output", nil }}}})
	engine := publicationEngine(t, deployment, store)
	root, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := root.Await(t.Context()); !errors.Is(awaitErr, loss) {
		t.Fatalf("await error=%v", awaitErr)
	}
	if joinErr := root.Join(t.Context()); !errors.Is(joinErr, loss) {
		t.Fatalf("join error=%v", joinErr)
	}
	if executions.Load() != 1 || len(store.entries()) != 1 {
		t.Fatal("known result lost")
	}
	path := store.path
	store.Close()
	store = openPublicationStore(t, path)
	if len(store.database.Publications) != 1 {
		t.Fatalf("publications=%d", len(store.database.Publications))
	}
	recovered := publicationEngine(t, deployment, store)
	restored, err := recovered.RestoreTree(t.Context(), deployment.Deployment, store.tree())
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := restored.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := restored.Await(t.Context())
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%v err=%v", result.Status(), err)
	}
	if executions.Load() != 1 || modelCalls.Load() != 2 || len(store.entries()) != 1 || publicationText(store.entries()[0]) != "exact output" {
		t.Fatal("recovery replayed or changed execution")
	}
	if len(store.database.Publications) != 1 {
		t.Fatal("writer change altered publication identity")
	}
}

func TestRejectionsAreDurableBeforeModelContinuation(t *testing.T) {
	const count = 32
	store := newPublicationStore(t)
	calls := make([]chat.ToolCall, count)
	for index := range calls {
		calls[index] = chat.ToolCall{ID: fmt.Sprintf("call_%d", index), Name: "missing", Arguments: `{}`}
	}
	modelCalls := 0
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Description: "Persist known results.", Name: "publication.rejections"}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		modelCalls++
		if modelCalls == 1 {
			return publicationResponse(calls...), nil
		}
		entries := store.entries()
		if len(entries) != count {
			return nil, fmt.Errorf("only %d persisted results", len(entries))
		}
		parts := request.Messages[len(request.Messages)-1].Parts
		if len(parts) != count {
			return nil, errors.New("fragmented model feedback")
		}
		for index, entry := range entries {
			if entry.Call != calls[index] || entry.ToolCallIndex != uint32(index) || entry.Disposition != interaction.ResultRejected || !reflect.DeepEqual(parts[index].ToolResult, &entry.Result) {
				return nil, fmt.Errorf("wrong result %d", index)
			}
		}
		return textResponse("done"), nil
	})}, interaction.ToolSetConfig{})
	engine := publicationEngine(t, deployment, store)
	result, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if err != nil || result.Status() != agent.StatusCompleted || modelCalls != 2 {
		t.Fatalf("result=%s err=%v calls=%d", result.Status(), err, modelCalls)
	}
}

func TestPublicationConflictAndStaleWriterAreRejected(t *testing.T) {
	store := newPublicationStore(t)
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Description: "Persist known results.", Name: "publication.fence"}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return textResponse("done"), nil })}, interaction.ToolSetConfig{})
	engine := publicationEngine(t, deployment, store)
	result, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatal(err)
	}
	old := store.tree()
	restoredEngine := publicationEngine(t, deployment, store)
	root, err := restoredEngine.RestoreTree(t.Context(), deployment.Deployment, old)
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := root.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	current := store.tree()
	writer, _ := old.IncarnationID()
	if err := store.commit(old, old.Digest(), writer, "stale"); !errors.Is(err, agent.ErrTreeIncarnationConflict) {
		t.Fatalf("stale error=%v", err)
	}
	if store.tree().Digest() != current.Digest() {
		t.Fatal("stale writer changed head")
	}
	original := []byte(`{"result":"confirmed"}`)
	id := agent.ComputeDigest(original)
	if err := store.database.recordPublication(id, original); err != nil {
		t.Fatal(err)
	}
	if err := store.database.recordPublication(id, []byte(`{"result":"changed"}`)); !errors.Is(err, agent.ErrDurabilityConflict) {
		t.Fatalf("conflict=%v", err)
	}
	if string(store.database.Publications[id.String()]) != string(original) {
		t.Fatal("conflict corrupted original")
	}
}

func TestPublicationFailureBlocksContinuationAndReleaseDrains(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct=%v", direct), func(t *testing.T) {
			store := newPublicationStore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			failure := errors.New("host storage released")
			store.before = func(_ agent.TreeSnapshot, publications []interaction.RoundResults) error {
				if len(publications) > 0 {
					close(entered)
					<-release
					return failure
				}
				return nil
			}
			var modelCalls atomic.Int32
			var executable tool.Tool = &callbackTool{name: "read", call: func(context.Context, string) (string, error) { return "known", nil }}
			if direct {
				executable = directTool{Tool: executable}
			}
			deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.block", Description: "Block publication before continuation."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				modelCalls.Add(1)
				return toolCallResponse(chat.ToolCall{ID: "call", Name: "read", Arguments: `{}`}), nil
			})}, interaction.ToolSetConfig{Tools: []tool.Tool{executable}})
			engine := publicationEngine(t, deployment, store)
			root, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "work"))
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			waitContext, stopWait := context.WithCancel(t.Context())
			stopWait()
			if err := root.Join(waitContext); !errors.Is(err, context.Canceled) {
				t.Fatalf("join=%v", err)
			}
			if modelCalls.Load() != 1 {
				t.Fatal("model continued while publication blocked")
			}
			once.Do(func() { close(release) })
			if joinErr := root.Join(t.Context()); !errors.Is(joinErr, failure) {
				t.Fatalf("join=%v", joinErr)
			}
			if err := engine.ReleaseTree(t.Context(), root.ID()); err != nil && !errors.Is(err, failure) {
				t.Fatal(err)
			}
		})
	}
}

func TestDirectCompletionPersistsExactResult(t *testing.T) {
	store := newPublicationStore(t)
	var modelCalls atomic.Int32
	executable := directTool{Tool: &callbackTool{name: "read", call: func(context.Context, string) (string, error) { return "direct output", nil }}}
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.direct", Description: "Persist direct completion."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls.Add(1)
		return toolCallResponse(chat.ToolCall{ID: "call", Name: "read", Arguments: `{}`}), nil
	})}, interaction.ToolSetConfig{Tools: []tool.Tool{executable}})
	engine := publicationEngine(t, deployment, store)
	result, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s err=%v", result.Status(), err)
	}
	if modelCalls.Load() != 1 || len(store.entries()) != 1 || publicationText(store.entries()[0]) != "direct output" {
		t.Fatal("direct completion did not preserve result")
	}
}

func TestCancellationBeforeDispatchPublishesNothing(t *testing.T) {
	store := newPublicationStore(t)
	var calls atomic.Int32
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.before", Description: "Cancel before external dispatch."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls.Add(1)
		return textResponse("unexpected"), nil
	})}, interaction.ToolSetConfig{})
	engine := publicationEngine(t, deployment, store)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "work"))
	if err == nil {
		if joinErr := root.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
	} else if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 || len(store.entries()) != 0 {
		t.Fatal("canceled start dispatched or fabricated results")
	}
}

func TestPublicationRetainsRefusalAndKnownFailureDuringCancellation(t *testing.T) {
	store := newPublicationStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	failureOutput := chat.NewTextToolOutput("first write confirmed; second write rejected")
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Output: failureOutput, Cause: errors.New("second write failed")})
	if err != nil {
		t.Fatal(err)
	}
	tools := []tool.Tool{&callbackTool{name: "failed", call: func(context.Context, string) (string, error) { return "", failure }}, &callbackTool{name: "blocked", call: func(context.Context, string) (string, error) {
		close(entered)
		<-release
		return "", errors.New("unknown")
	}}}
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.refusal", Description: "Retain refusal and failed output."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return publicationResponse(chat.ToolCall{ID: "refused", Name: "missing", Arguments: `{}`}, chat.ToolCall{ID: "failed", Name: "failed", Arguments: `{}`}, chat.ToolCall{ID: "blocked", Name: "blocked", Arguments: `{}`}), nil
	})}, interaction.ToolSetConfig{Tools: tools})
	engine := publicationEngine(t, deployment, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	if _, awaitErr := root.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	once.Do(func() { close(release) })
	if joinErr := root.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	entries := store.entries()
	if len(entries) != 2 || entries[0].Disposition != interaction.ResultRejected || entries[1].Disposition != interaction.ResultFailed || !reflect.DeepEqual(entries[1].Result.Output, failureOutput) {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestLocalRejectionCheckpointBlocksTailDispatch(t *testing.T) {
	store := newPublicationStore(t)
	failure := errors.New("refusal checkpoint unavailable")
	var toolCalls atomic.Int32
	store.before = func(_ agent.TreeSnapshot, publications []interaction.RoundResults) error {
		if len(publications) > 0 {
			publication := publications[0]
			original := publication.JSON()
			entries := publication.Entries()
			entries[0].Result.Output.Content[0].Text = "mutated"
			if !reflect.DeepEqual(original, publication.JSON()) || publicationText(publication.Entries()[0]) == "mutated" {
				return errors.New("mutable publication")
			}
			return failure
		}
		return nil
	}
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.local", Description: "Persist local refusal before dispatching tail."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return publicationResponse(chat.ToolCall{ID: "refused", Name: "missing", Arguments: `{}`}, chat.ToolCall{ID: "tail", Name: "read", Arguments: `{}`}), nil
	})}, interaction.ToolSetConfig{Tools: []tool.Tool{&callbackTool{name: "read", call: func(context.Context, string) (string, error) { toolCalls.Add(1); return "unexpected", nil }}}})
	engine := publicationEngine(t, deployment, store)
	_, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if !errors.Is(err, failure) || toolCalls.Load() != 0 {
		t.Fatalf("error=%v tail executions=%d", err, toolCalls.Load())
	}
}
