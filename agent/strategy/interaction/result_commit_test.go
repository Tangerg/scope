package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type resultCommitter struct {
	commit func(context.Context, interaction.ResultBatch) (interaction.ResultReceipt, error)
}

func (r *resultCommitter) CommitResults(ctx context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
	return r.commit(ctx, batch)
}

func TestRejectedToolBatchPreservesContinuationOrder(t *testing.T) {
	const count = 256
	calls := make([]chat.ToolCall, count)
	parts := make([]chat.Part, count)
	for index := range count {
		calls[index] = chat.ToolCall{ID: fmt.Sprintf("call_%d", index), Name: "unavailable", Arguments: `{}`}
		parts[index] = chat.NewToolCallPart(calls[index])
	}
	committed := make([]chat.ToolResult, 0, count)
	committer := &resultCommitter{commit: func(_ context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
		if batch.ModelCallSequence() != 1 || len(batch.Entries()) != count {
			return interaction.ResultReceipt{}, fmt.Errorf("incorrect batch attribution")
		}
		for index, entry := range batch.Entries() {
			if entry.ToolCallIndex != uint32(index) || entry.Call != calls[index] || entry.Disposition != interaction.ResultRejected {
				return interaction.ResultReceipt{}, fmt.Errorf("incorrect rejection attribution: %d", index)
			}
			committed = append(committed, entry.Result)
		}
		detached := batch.Entries()
		detached[0].Result.Output.Content[0].Text = "mutated"
		return batch.Receipt(), nil
	}}

	n := 0
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		n++
		if n == 1 {
			message := chat.NewAssistantMessage(parts...)
			return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, nil
		}
		if len(committed) != count {
			return nil, fmt.Errorf("model advanced after %d commits", len(committed))
		}
		messages := request.Messages
		if len(messages) != 3 || len(messages[1].Parts) != count || len(messages[2].Parts) != count {
			return nil, fmt.Errorf("wrong continuation shape")
		}
		for index, part := range messages[2].Parts {
			if part.ToolResult == nil || !reflect.DeepEqual(*part.ToolResult, committed[index]) {
				return nil, fmt.Errorf("result %d differs from committed outcome", index)
			}
		}
		return textResponse("done"), nil
	})
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "rejection.order", Description: "Commit rejections in order.", MaxModelCalls: 2}, interaction.DispatcherConfig{Model: model, ResultCommitter: committer}, interaction.ToolSetConfig{})
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := engine.Close(context.WithoutCancel(t.Context())); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	result, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status=%s termination=%+v", result.Status(), result.Termination())
	}
}

func TestToolSetCommitsValidationRejectionBeforeContinuing(t *testing.T) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "read", Description: "Read a file."}, func(context.Context, struct {
		Path string `json:"path"`
	}) (string, error) {
		t.Error("rejected call executed")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var committed *chat.ToolResult
	committer := &resultCommitter{commit: func(_ context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
		entries := batch.Entries()
		if len(entries) != 1 || entries[0].Disposition != interaction.ResultRejected {
			return interaction.ResultReceipt{}, fmt.Errorf("incorrect rejection")
		}
		committed = &entries[0].Result
		return batch.Receipt(), nil
	}}

	n := 0
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		n++
		if n == 1 {
			return toolCallResponse(chat.ToolCall{ID: "rejected", Name: "read", Arguments: `{"path":`}), nil
		}
		result := request.Messages[len(request.Messages)-1].Parts[0].ToolResult
		if committed == nil || !reflect.DeepEqual(result, committed) {
			return nil, fmt.Errorf("uncommitted rejection")
		}
		return textResponse("done"), nil
	})
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "rejection.validation", Description: "Commit validation rejection.", MaxModelCalls: 2}, interaction.DispatcherConfig{Model: model, ResultCommitter: committer}, interaction.ToolSetConfig{Tools: []tool.Tool{executable}})
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := engine.Close(context.WithoutCancel(t.Context())); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	result, err := engine.Run(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status=%s termination=%+v", result.Status(), result.Termination())
	}
}

func TestRejectionCommitFailureStopsBeforeAnotherModelCall(t *testing.T) {
	for _, name := range []string{"unavailable", "read"} {
		t.Run(name, func(t *testing.T) {
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "read", Description: "Read a file."}, func(context.Context, struct {
				Path string `json:"path"`
			}) (string, error) {
				t.Error("rejected call executed")
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("rejection transaction unavailable")
			committer := &resultCommitter{commit: func(context.Context, interaction.ResultBatch) (interaction.ResultReceipt, error) {
				return interaction.ResultReceipt{}, cause
			}}
			model := &singleToolCallModel{call: chat.ToolCall{ID: "rejected", Name: name, Arguments: `{"wrong":true}`}}
			deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "rejection.failure", Description: "Stop on a failed rejection commit.", MaxModelCalls: 2}, interaction.DispatcherConfig{Model: model, ResultCommitter: committer}, interaction.ToolSetConfig{Tools: []tool.Tool{executable}})
			events := &agenttest.ObservationRecorder{}
			engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver, EventListeners: []agent.EventListener{events}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cleanupErr := engine.Close(context.WithoutCancel(t.Context())); cleanupErr != nil {
					t.Error(cleanupErr)
				}
			})
			process, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "work"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cleanupErr := process.Kill(context.WithoutCancel(t.Context()), "test complete"); cleanupErr != nil && !errors.Is(cleanupErr, agent.ErrProcessFinished) {
					t.Error(cleanupErr)
				}
				if _, cleanupErr := process.Await(context.WithoutCancel(t.Context())); cleanupErr != nil {
					t.Error(cleanupErr)
				}
				if cleanupErr := engine.ReleaseTree(context.WithoutCancel(t.Context()), process.ID()); cleanupErr != nil {
					t.Error(cleanupErr)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			event, err := events.AwaitEvent(ctx, func(event agent.Event) bool {
				fact, ok := event.EffectFinished()
				return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown
			})
			if err != nil {
				t.Fatal(err)
			}
			owner, ok := engine.Process(event.Relation().ProcessID())
			if !ok {
				t.Fatal("unknown Effect owner missing")
			}
			snapshot := inspectProcessSnapshot(t, engine, owner)
			ids := snapshot.UnknownEffectIDs()
			if len(ids) != 1 {
				t.Fatalf("unknown Effects=%v", ids)
			}
			failure, found := snapshot.EffectDiagnostic(ids[0])
			if !found || failure.Code() != "engine.dispatch.failed" {
				t.Fatalf("failure=%+v found=%t", failure, found)
			}
			if model.Calls() != 1 {
				t.Fatalf("model advanced after commit failed: %d", model.Calls())
			}
		})
	}
}

// Independent witnesses distinguish external execution, host publication, and
// model adoption. Every crash cut must preserve all three, including direct
// completion where there is no following model call to expose early adoption.
func TestResultPublicationRecoveryNeverReexecutesKnownTools(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, cut := range []string{"write_failed", "receipt_lost", "wrong_receipt", "settlement_receipt_lost"} {
			t.Run(fmt.Sprintf("direct_%t/%s", direct, cut), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				var executions, publications, modelCalls atomic.Int32
				output := chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartText, Text: "exact result\n"}}, Details: json.RawMessage(`{"value":42}`)}
				executable := &publicationTool{output: output, executions: &executions}
				var bound tool.Tool = executable
				if direct {
					bound = directTool{Tool: executable}
				}
				var pending interaction.ResultBatch
				var stored *interaction.ResultReceipt
				var storedResult chat.ToolResult
				committer := &resultCommitter{commit: func(_ context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
					publications.Add(1)
					pending = batch
					entries := batch.Entries()
					if len(entries) != 1 || entries[0].Disposition != interaction.ResultSucceeded || executions.Load() != 1 {
						return interaction.ResultReceipt{}, errors.New("invalid execution evidence")
					}
					if !reflect.DeepEqual(entries[0].Result.Output, output) {
						return interaction.ResultReceipt{}, errors.New("publication changed executor output")
					}
					receipt := batch.Receipt()
					if cut == "write_failed" {
						return interaction.ResultReceipt{}, errors.New("write rejected")
					}
					stored, storedResult = new(receipt), entries[0].Result
					if cut == "receipt_lost" {
						return interaction.ResultReceipt{}, errors.New("host receipt lost")
					}
					if cut == "wrong_receipt" {
						receipt.Digest = agent.ComputeDigest([]byte("wrong content"))
					}
					return receipt, nil
				}}
				model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
					if modelCalls.Add(1) == 1 {
						return toolCallResponse(chat.ToolCall{ID: "provider_reused", Name: "work", Arguments: `{}`}), nil
					}
					result := request.Messages[len(request.Messages)-1].Parts[0].ToolResult
					if stored == nil || result == nil || !reflect.DeepEqual(*result, storedResult) {
						return nil, errors.New("uncommitted or changed result reached model")
					}
					return textResponse("accepted"), nil
				})
				deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.recovery", Description: "Recover only result publication.", MaxModelCalls: 2}, interaction.DispatcherConfig{Model: model, ResultCommitter: committer}, interaction.ToolSetConfig{Tools: []tool.Tool{bound}})
				store := agenttest.NewMemoryTreeDurability()
				crash := &resultPublicationCrash{MemoryTreeDurability: store}
				engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: crash, DeploymentResolver: deployment.resolver})
				if err != nil {
					t.Fatal(err)
				}
				root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "work"))
				if err != nil {
					t.Fatal(err)
				}
				result, err := root.Await(ctx)
				if _, stopped := errors.AsType[*agent.RuntimeError](err); !stopped || result.Valid() {
					t.Fatalf("crash returned result=%s err=%v", result.Status(), err)
				}
				head, found, err := store.LoadTree(ctx, root.ID())
				if err != nil || !found {
					t.Fatalf("head=%t err=%v", found, err)
				}
				if executions.Load() != 1 || publications.Load() != 1 || modelCalls.Load() != 1 {
					t.Fatal("publication boundary was bypassed")
				}
				if releaseErr := engine.ReleaseTree(ctx, root.ID()); releaseErr != nil {
					t.Fatal(releaseErr)
				}
				if closeErr := engine.Close(context.WithoutCancel(ctx)); closeErr != nil {
					t.Fatal(closeErr)
				}
				restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store, DeploymentResolver: deployment.resolver})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if closeErr := restoredEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
						t.Error(closeErr)
					}
				})
				restored, err := restoredEngine.RestoreTree(ctx, deployment.Deployment, head)
				if err != nil {
					t.Fatal(err)
				}
				if cut != "settlement_receipt_lost" {
					snapshot := inspectProcessSnapshot(t, restoredEngine, restored)
					if ids := snapshot.UnknownEffectIDs(); len(ids) != 1 || ids[0] != pending.Receipt().EffectID {
						t.Fatalf("publication identity=%v", ids)
					}
					if stored == nil {
						receipt := pending.Receipt()
						stored = &receipt
						storedResult = pending.Entries()[0].Result
					}
					settlement, settlementErr := stored.Settlement()
					if settlementErr != nil {
						t.Fatal(settlementErr)
					}
					if resolveErr := restored.ResolveUnknownEffect(ctx, settlement); resolveErr != nil {
						t.Fatal(resolveErr)
					}
				}
				completed, err := restored.Await(ctx)
				if err != nil || completed.Status() != agent.StatusCompleted {
					t.Fatalf("recovered status=%s termination=%v err=%v", completed.Status(), completed.Termination(), err)
				}
				wantCalls := int32(2)
				if direct {
					wantCalls = 1
				}
				if executions.Load() != 1 || publications.Load() != 1 || modelCalls.Load() != wantCalls {
					t.Fatal("recovery repeated external work or publication")
				}
				if direct {
					encoded, _ := completed.Output()
					output, err := encoded.Decode[interaction.Output]()
					if err != nil || len(output.DirectToolResults) != 1 || !reflect.DeepEqual(output.DirectToolResults[0], storedResult) {
						t.Fatalf("direct result changed: %+v err=%v", output, err)
					}
				}
			})
		}
	}
}

func TestResultPublicationCoversMixedToolAndDelegatePaths(t *testing.T) {
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
		delegate, err := interaction.NewDelegate(interaction.DelegateConfig{Name: name, Description: "Publish delegated outcomes.", Deployment: deployment, Budget: agent.Budget{Steps: 8, Effects: 8, Signals: 8}})
		if err != nil {
			t.Fatal(err)
		}
		delegates = append(delegates, delegate)
	}
	partialOutput := chat.NewTextToolOutput("one external write completed\n" + strings.Repeat("界", 900))
	partial, err := tool.NewFailure(errors.New("remaining writes failed"), partialOutput)
	if err != nil {
		t.Fatal(err)
	}
	tools := []tool.Tool{
		&callbackTool{name: "success", call: func(context.Context, string) (string, error) { toolsExecuted.Add(1); return "ordinary output", nil }},
		&callbackTool{name: "partial", call: func(context.Context, string) (string, error) { toolsExecuted.Add(1); return "", partial }},
		&callbackTool{name: "denied", call: func(context.Context, string) (string, error) { return "", tool.ErrAuthorizationDenied }},
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
	var committed []chat.ToolResult
	publications, modelCalls := 0, 0
	committer := &resultCommitter{commit: func(_ context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
		publications++
		if len(batch.Entries()) != len(calls) || toolsExecuted.Load() != 2 || workersExecuted.Load() != 2 {
			return interaction.ResultReceipt{}, errors.New("incomplete round published")
		}
		for index, entry := range batch.Entries() {
			if entry.Call != calls[index] || entry.ToolCallIndex != uint32(index) || entry.Disposition != wantDisposition[index] {
				return interaction.ResultReceipt{}, fmt.Errorf("wrong result attribution: %d", index)
			}
			if index == 0 && !reflect.DeepEqual(entry.Result.Output, chat.NewTextToolOutput("ordinary output")) ||
				index == 6 && !reflect.DeepEqual(entry.Result.Output, partialOutput) {
				return interaction.ResultReceipt{}, errors.New("known executor output changed")
			}
			if index == 5 {
				text, _ := entry.Result.Output.Text()
				if len(text) < 2048 || !utf8.ValidString(text) {
					return interaction.ResultReceipt{}, errors.New("invalid long worker diagnostic")
				}
			}
			committed = append(committed, entry.Result)
		}
		return batch.Receipt(), nil
	}}
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
		if publications != 1 || len(committed) != len(calls) {
			return nil, errors.New("model advanced before publication")
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
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.mixed", Description: "Commit all known result producers.", MaxModelCalls: 2, Delegates: delegates}, interaction.DispatcherConfig{Model: model, ResultCommitter: committer}, interaction.ToolSetConfig{Tools: tools})
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolveWith(worker)})
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
	if err != nil || result.Status() != agent.StatusCompleted || modelCalls != 2 || publications != 1 {
		t.Fatalf("mixed publication result=%s err=%v model=%d publications=%d", result.Status(), err, modelCalls, publications)
	}
}

type resultPublicationCrash struct {
	*agenttest.MemoryTreeDurability
}

type publicationTool struct {
	output     chat.ToolOutput
	executions *atomic.Int32
}

func (p *publicationTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{Name: "work", Description: "Produce a known result.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
}

func (p *publicationTool) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	p.executions.Add(1)
	return p.output.Clone(), nil
}

func (r *resultPublicationCrash) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if err := r.MemoryTreeDurability.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	var intent struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(boundary.Request().Effect().Payload(), &intent); err != nil {
		return err
	}
	if _, settled := boundary.Settlement(); settled && intent.Operation == "result_commit" {
		return errors.New("crash after publication settlement committed")
	}
	return nil
}
