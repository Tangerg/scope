package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
		for _, cut := range []string{"before_commit", "write_failed", "receipt_lost", "wrong_receipt", "settlement_receipt_lost"} {
			t.Run(fmt.Sprintf("direct_%t/%s", direct, cut), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				witness := &publicationWitness{MemoryTreeDurability: agenttest.NewMemoryTreeDurability()}
				head := crashPublication(t, ctx, witness, direct, cut)
				var snapshot agent.TreeSnapshot
				if decodeErr := json.Unmarshal(head, &snapshot); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				// Only durable bytes and independent external witnesses cross the restart.
				deployment := publicationDeployment(t, witness, direct, &resultCommitter{commit: witness.commit})
				engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: witness, DeploymentResolver: deployment.resolver})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
						t.Error(closeErr)
					}
				})
				root, err := engine.RestoreTree(ctx, deployment.Deployment, snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if cut != "before_commit" && cut != "settlement_receipt_lost" {
					ids := inspectProcessSnapshot(t, engine, root).UnknownEffectIDs()
					if len(ids) != 1 {
						t.Fatalf("unknown publications=%v", ids)
					}
					if replayErr := root.ReplayUnknownEffect(ctx, ids[0]); replayErr != nil {
						t.Fatal(replayErr)
					}
				}
				completed, err := root.Await(ctx)
				if err != nil || completed.Status() != agent.StatusCompleted {
					t.Fatalf("status=%s termination=%v err=%v", completed.Status(), completed.Termination(), err)
				}
				if joinErr := root.Join(ctx); joinErr != nil {
					t.Fatal(joinErr)
				}
				wantCalls := int32(2)
				if direct {
					wantCalls = 1
				}
				if witness.executions.Load() != 1 || witness.writes.Load() != 1 || witness.modelCalls.Load() != wantCalls {
					t.Fatalf("executions=%d writes=%d model=%d", witness.executions.Load(), witness.writes.Load(), witness.modelCalls.Load())
				}
				if direct {
					encoded, _ := completed.Output()
					output, err := encoded.Decode[interaction.Output]()
					stored := witness.record()
					if err != nil || len(output.DirectToolResults) != 1 || !reflect.DeepEqual(output.DirectToolResults[0], stored.Result) {
						t.Fatalf("direct output=%+v err=%v", output, err)
					}
				}
			})
		}
	}
}

type publicationRecord struct {
	Receipt interaction.ResultReceipt
	Result  chat.ToolResult
}

type publicationWitness struct {
	*agenttest.MemoryTreeDurability
	mu          sync.Mutex
	publication []byte
	executions  atomic.Int32
	writes      atomic.Int32
	modelCalls  atomic.Int32
}

// Publication and writer activation share one transaction lock, so a writer
// cannot pass the fence and then commit after another incarnation takes over.
func (p *publicationWitness) ActivateTree(ctx context.Context, activation agent.TreeActivation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.MemoryTreeDurability.ActivateTree(ctx, activation)
}

func (p *publicationWitness) record() publicationRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	var record publicationRecord
	if len(p.publication) != 0 {
		if decodeErr := json.Unmarshal(p.publication, &record); decodeErr != nil {
			panic(decodeErr)
		}
	}
	return record
}

func (p *publicationWitness) commit(ctx context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	head, found, err := p.LoadTree(ctx, batch.Relation().RootID())
	if err != nil || !found {
		return interaction.ResultReceipt{}, errors.New("missing authoritative tree")
	}
	writer, _ := head.IncarnationID()
	requestedWriter, _ := batch.TreeIncarnationID()
	if writer != requestedWriter {
		return interaction.ResultReceipt{}, agent.ErrTreeIncarnationConflict
	}
	entries := batch.Entries()
	if batch.ModelCallSequence() != 1 || len(entries) != 1 || entries[0].ToolCallIndex != 0 || entries[0].Disposition != interaction.ResultSucceeded || p.executions.Load() != 1 {
		return interaction.ResultReceipt{}, errors.New("incorrect recovered batch")
	}
	if len(p.publication) != 0 {
		var stored publicationRecord
		if decodeErr := json.Unmarshal(p.publication, &stored); decodeErr != nil {
			return interaction.ResultReceipt{}, decodeErr
		}
		if stored.Receipt != batch.Receipt() || !reflect.DeepEqual(stored.Result, entries[0].Result) {
			return interaction.ResultReceipt{}, errors.New("conflicting publication")
		}
		return stored.Receipt, nil
	}
	p.publication, err = json.Marshal(publicationRecord{Receipt: batch.Receipt(), Result: entries[0].Result})
	if err != nil {
		return interaction.ResultReceipt{}, err
	}
	p.writes.Add(1)
	return batch.Receipt(), nil
}

func publicationDeployment(t *testing.T, witness *publicationWitness, direct bool, committer interaction.ResultCommitter) interactionDeployment {
	t.Helper()
	executable := &publicationTool{output: chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartText, Text: "exact result\n"}}, Details: json.RawMessage(`{"value":42}`)}, executions: &witness.executions}
	var bound tool.Tool = executable
	if direct {
		bound = directTool{Tool: executable}
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if witness.modelCalls.Add(1) == 1 {
			return toolCallResponse(chat.ToolCall{ID: "provider_reused", Name: "work", Arguments: `{}`}), nil
		}
		stored := witness.record()
		result := request.Messages[len(request.Messages)-1].Parts[0].ToolResult
		if stored.Receipt.Validate() != nil || result == nil || !reflect.DeepEqual(*result, stored.Result) {
			return nil, errors.New("uncommitted or changed result reached model")
		}
		return textResponse("accepted"), nil
	})
	return configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.recovery", Description: "Recover only result publication.", MaxModelCalls: 2}, interaction.DispatcherConfig{Model: model, ResultCommitter: committer}, interaction.ToolSetConfig{Tools: []tool.Tool{bound}})
}

func crashPublication(t *testing.T, ctx context.Context, witness *publicationWitness, direct bool, cut string) []byte {
	t.Helper()
	var publications atomic.Int32
	committer := &resultCommitter{commit: func(ctx context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
		publications.Add(1)
		if cut == "write_failed" {
			return interaction.ResultReceipt{}, errors.New("write rejected")
		}
		receipt, err := witness.commit(ctx, batch)
		if err != nil {
			return interaction.ResultReceipt{}, err
		}
		if cut == "receipt_lost" {
			return interaction.ResultReceipt{}, errors.New("receipt lost")
		}
		if cut == "wrong_receipt" {
			receipt.Digest = agent.ComputeDigest([]byte("wrong content"))
		}
		return receipt, nil
	}}
	deployment := publicationDeployment(t, witness, direct, committer)
	crash := &resultPublicationCrash{MemoryTreeDurability: witness.MemoryTreeDurability, beforeCommit: cut == "before_commit"}
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
		t.Fatalf("crash result=%s err=%v", result.Status(), err)
	}
	head, found, err := witness.LoadTree(ctx, root.ID())
	if err != nil || !found {
		t.Fatalf("head=%t err=%v", found, err)
	}
	wantPublications := int32(1)
	if cut == "before_commit" {
		wantPublications = 0
	}
	if witness.executions.Load() != 1 || publications.Load() != wantPublications || witness.modelCalls.Load() != 1 {
		t.Fatal("publication boundary bypassed")
	}
	if err := engine.ReleaseTree(ctx, root.ID()); err != nil {
		t.Fatal(err)
	}
	if closeErr := engine.Close(context.WithoutCancel(ctx)); closeErr != nil {
		t.Fatal(closeErr)
	}
	return head.JSON()
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
	beforeCommit bool
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
	if decodeErr := json.Unmarshal(boundary.Request().Effect().Payload(), &intent); decodeErr != nil {
		return decodeErr
	}
	_, settled := boundary.Settlement()
	if intent.Operation == "result_commit" && (r.beforeCommit && boundary.Kind() == agent.EffectBoundaryPending || !r.beforeCommit && settled) {
		return errors.New("crash after publication settlement committed")
	}
	return nil
}

func TestPublicationReplaySurvivesWriterTakeover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		witness := &publicationWitness{MemoryTreeDurability: agenttest.NewMemoryTreeDurability()}
		var snapshot agent.TreeSnapshot
		if decodeErr := json.Unmarshal(crashPublication(t, t.Context(), witness, false, "write_failed"), &snapshot); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		oldDeployment := publicationDeployment(t, witness, false, &resultCommitter{commit: func(ctx context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
			close(entered)
			<-release
			return witness.commit(ctx, batch)
		}})
		oldEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: witness, DeploymentResolver: oldDeployment.resolver})
		if err != nil {
			t.Fatal(err)
		}
		oldRoot, err := oldEngine.RestoreTree(t.Context(), oldDeployment.Deployment, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		ids := inspectProcessSnapshot(t, oldEngine, oldRoot).UnknownEffectIDs()
		if len(ids) != 1 {
			t.Fatal(ids)
		}
		replayed := make(chan error, 1)
		go func() { replayed <- oldRoot.ReplayUnknownEffect(t.Context(), ids[0]) }()
		<-entered
		synctest.Wait()
		head, found, err := witness.LoadTree(t.Context(), oldRoot.ID())
		if err != nil || !found {
			t.Fatalf("head=%t err=%v", found, err)
		}
		if decodeErr := json.Unmarshal(head.JSON(), &snapshot); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		newDeployment := publicationDeployment(t, witness, false, &resultCommitter{commit: witness.commit})
		newEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: witness, DeploymentResolver: newDeployment.resolver})
		if err != nil {
			t.Fatal(err)
		}
		newRoot, err := newEngine.RestoreTree(t.Context(), newDeployment.Deployment, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if replayErr := newRoot.ReplayUnknownEffect(t.Context(), ids[0]); replayErr != nil {
			t.Fatal(replayErr)
		}
		result, err := newRoot.Await(t.Context())
		if err != nil || result.Status() != agent.StatusCompleted {
			t.Fatalf("new writer: %s %v", result.Status(), err)
		}
		if joinErr := newRoot.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		unblock()
		if err := <-replayed; !errors.Is(err, agent.ErrTreeIncarnationConflict) || !errors.Is(err, agent.ErrEffectOutcomeUnknown) {
			t.Fatalf("stale writer replay: %v", err)
		}
		if witness.writes.Load() != 1 || witness.executions.Load() != 1 || witness.modelCalls.Load() != 2 {
			t.Fatal("takeover repeated business work")
		}
		if killErr := oldRoot.Kill(t.Context(), "retire stale writer"); killErr != nil {
			t.Fatal(killErr)
		}
		if _, awaitErr := oldRoot.Await(t.Context()); awaitErr == nil {
			t.Fatal("stale writer committed termination")
		}
		if closeErr := oldEngine.Close(t.Context()); closeErr != nil {
			t.Fatal(closeErr)
		}
		if closeErr := newEngine.Close(t.Context()); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
}
