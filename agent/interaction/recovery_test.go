package interaction

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

var errToolSettlementCrash = errors.New("tool settlement writer stopped")

type toolSettlementBarrier struct {
	store     run.Store
	committed bool
	blocked   bool
	tool      run.EffectID
	reached   chan run.EffectID
	release   chan struct{}
	once      sync.Once
}

func (t *toolSettlementBarrier) Append(ctx context.Context, batch run.Batch) error {
	for _, record := range batch.Records {
		if intent, ok := record.(run.EffectIntent); ok && intent.Effect.Target() == run.EffectTargetDispatcher {
			_, _, isTool, err := ToolCallFromEffect(intent.Effect)
			if err != nil {
				return err
			}
			if isTool {
				t.tool = intent.ID
			}
		}
	}
	selected := false
	for _, record := range batch.Records {
		if settled, ok := record.(run.EffectSettled); ok && settled.ID == t.tool && !t.blocked {
			selected = true
			t.blocked = true
			break
		}
	}
	if !selected {
		return t.store.Append(ctx, batch)
	}
	if t.committed {
		if err := t.store.Append(ctx, batch); err != nil {
			return err
		}
	}
	t.reached <- t.tool
	select {
	case <-t.release:
		return errToolSettlementCrash
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *toolSettlementBarrier) Load(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
	return t.store.Load(ctx, root)
}
func (t *toolSettlementBarrier) stop() { t.once.Do(func() { close(t.release) }) }

func TestToolSettlementCrashRespectsCommittedOutcomeAndReplayPolicy(t *testing.T) {
	for _, policy := range []run.ReplayPolicy{run.ReplayPolicyNever, run.ReplayPolicySameIdentity} {
		for _, committed := range []bool{false, true} {
			name := policy.String() + "/before-append"
			if committed {
				name = policy.String() + "/after-append"
			}
			t.Run(name, func(t *testing.T) {
				store := run.NewMemoryStore()
				barrier := &toolSettlementBarrier{store: store, committed: committed, reached: make(chan run.EffectID, 1), release: make(chan struct{})}
				defer barrier.stop()
				original := conformancetest.Runtime(t, run.Config{Store: barrier})
				var toolCalls, modelCalls atomic.Int32
				var idsMu sync.Mutex
				var ids []run.EffectID
				executable := scriptTool{name: "write", direct: true, invoke: func(ctx context.Context, _ tool.Invocation) (tool.Result, error) {
					toolCalls.Add(1)
					invocation, _ := ToolInvocationFromContext(ctx)
					idsMu.Lock()
					ids = append(ids, invocation.EffectID())
					idsMu.Unlock()
					return tool.Result{Output: tool.NewTextOutput("verified")}, nil
				}}
				tools := mustValue(NewToolSet(ToolSetConfig{Revision: "idempotent-write", Replay: policy, Tools: []tool.Tool{executable}}))
				program := compileTest(t, original, Config{Tools: tools, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					modelCalls.Add(1)
					return callResponse(requested("write", "call")), nil
				})})
				handle := startInteraction(t, original, program)
				ctx, cancel := context.WithTimeout(t.Context(), interactionTestTimeout)
				defer cancel()
				var intended run.EffectID
				select {
				case intended = <-barrier.reached:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				recoveredRuntime := conformancetest.Runtime(t, run.Config{Store: store})
				recovered := mustValue(recoveredRuntime.Recover(ctx, program, handle.ID()))
				if !committed && policy == run.ReplayPolicyNever {
					log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
						for _, entry := range log.Entries {
							if settled, ok := entry.Record.(run.EffectSettled); ok && settled.ID == intended && settled.Outcome.Status() == run.SettlementStatusUnknown {
								return true
							}
						}
						return false
					})
					view := mustValue(log.Inspect())
					process, _ := view.Process(handle.ID())
					if len(process.PendingEffects) != 1 {
						t.Fatal("unknown tool obligation lost")
					}
					request := process.PendingEffects[0].Request
					settlement := mustValue(SettleToolResult(program, request, tool.Result{Output: tool.NewTextOutput("verified")}, ResultSucceeded, nil))
					if err := recovered.ResolveUnknownEffect(ctx, intended, settlement); err != nil {
						t.Fatal(err)
					}
				}
				output := awaitOutput(t, recovered)
				if len(output.DirectToolResults) != 1 {
					t.Fatal("tool output not recovered")
				}
				expected := int32(1)
				if !committed && policy == run.ReplayPolicySameIdentity {
					expected = 2
				}
				if toolCalls.Load() != expected || modelCalls.Load() != 1 {
					t.Fatalf("tools=%d models=%d", toolCalls.Load(), modelCalls.Load())
				}
				idsMu.Lock()
				for _, id := range ids {
					if id != intended {
						t.Error("replay changed logical effect identity")
					}
				}
				idsMu.Unlock()
				barrier.stop()
				if _, err := handle.Await(ctx); !errors.Is(err, errToolSettlementCrash) {
					t.Fatalf("old writer result=%v", err)
				}
			})
		}
	}
}

func TestToolInputWaitRecoversWithoutRepeatingItsEffect(t *testing.T) {
	store := run.NewMemoryStore()
	original := conformancetest.Runtime(t, run.Config{Store: store, CheckpointInterval: 1})
	var calls atomic.Int32
	executable := scriptTool{name: "approve", direct: true, invoke: func(_ context.Context, input tool.Invocation) (tool.Result, error) {
		calls.Add(1)
		if continuation, resumed := input.Continuation(); resumed {
			if string(continuation.Response()) != `"yes"` {
				return tool.Result{}, errors.New("changed answer")
			}
			return tool.Result{Output: tool.NewTextOutput("approved")}, nil
		}
		question := mustValue(newInputRequest([]byte(`"approve?"`), []byte(`{"type":"string"}`), []byte(`{}`)))
		return tool.Result{InputRequest: &question}, nil
	}}
	program := compileTest(t, original, Config{Tools: toolSetTest(t, executable), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("approve", "call")), nil
	})})
	handle := startInteraction(t, original, program)
	var wait PendingToolInput
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		inputs, err := PendingToolInputs(program, log)
		if err != nil {
			t.Fatal(err)
		}
		if len(inputs) == 1 {
			wait = inputs[0]
			return true
		}
		return false
	})
	if err := original.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restoredRuntime := conformancetest.Runtime(t, run.Config{Store: store})
	restored := mustValue(restoredRuntime.Recover(t.Context(), program, handle.ID()))
	view := mustValue(restored.Inspect(t.Context()))
	process, _ := view.Process(handle.ID())
	if len(process.Waits) != 1 || process.Waits[0].Key != wait.WaitKey() || calls.Load() != 1 {
		t.Fatal("recovery repeated the question effect or changed its identity")
	}
	if err := restored.Answer(t.Context(), wait.WaitKey(), "yes"); err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, restored)
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestLostToolQuestionCanBeResolvedWithoutRepeatingTheCall(t *testing.T) {
	store := run.NewMemoryStore()
	barrier := &toolSettlementBarrier{store: store, reached: make(chan run.EffectID, 1), release: make(chan struct{})}
	defer barrier.stop()
	original := conformancetest.Runtime(t, run.Config{Store: barrier})
	firstQuestion := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`{"phase":1}`)))
	secondQuestion := mustValue(newInputRequest([]byte(`"Give a reason."`), []byte(`{"type":"string"}`), []byte(`{"phase":2}`)))
	invocations := make(chan ToolInvocation, 3)
	var initialCalls, modelCalls atomic.Int32
	executable := scriptTool{name: "approve", direct: true, invoke: func(ctx context.Context, input tool.Invocation) (tool.Result, error) {
		invocation, present := ToolInvocationFromContext(ctx)
		if !present {
			return tool.Result{}, errors.New("missing tool invocation")
		}
		invocations <- invocation
		continuation, resumed := input.Continuation()
		if !resumed {
			initialCalls.Add(1)
			return tool.Result{InputRequest: &firstQuestion}, nil
		}
		switch string(continuation.State()) {
		case `{"phase":1}`:
			if string(continuation.Response()) != `true` {
				return tool.Result{}, errors.New("first answer changed")
			}
			return tool.Result{InputRequest: &secondQuestion}, nil
		case `{"phase":2}`:
			if string(continuation.Response()) != `"reviewed"` {
				return tool.Result{}, errors.New("second answer changed")
			}
			return tool.Result{Output: tool.NewTextOutput("approved after review")}, nil
		default:
			return tool.Result{}, errors.New("continuation state changed")
		}
	}}
	program := compileTest(t, original, Config{Tools: toolSetTest(t, executable), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls.Add(1)
		return callResponse(requested("approve", "call")), nil
	})})
	handle := startInteraction(t, original, program)
	ctx, cancel := context.WithTimeout(t.Context(), interactionTestTimeout)
	defer cancel()
	var intended run.EffectID
	select {
	case intended = <-barrier.reached:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	replacement := conformancetest.Runtime(t, run.Config{Store: store})
	recovered := mustValue(replacement.Recover(ctx, program, handle.ID()))
	log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		view := mustValue(log.Inspect())
		process, _ := view.Process(handle.ID())
		return len(process.PendingEffects) == 1 && process.PendingEffects[0].Outcome.Status() == run.SettlementStatusUnknown
	})
	if inputs := mustValue(PendingToolInputs(program, log)); len(inputs) != 0 {
		t.Fatal("uncommitted question became answerable")
	}
	view := mustValue(log.Inspect())
	process, _ := view.Process(handle.ID())
	request := process.PendingEffects[0].Request
	resolved := mustValue(SettleToolResult(program, request, tool.Result{InputRequest: &firstQuestion}, ResultSucceeded, nil))
	if err := recovered.ResolveUnknownEffect(ctx, intended, resolved); err != nil {
		t.Fatal(err)
	}
	var firstWait, secondWait PendingToolInput
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		inputs := mustValue(PendingToolInputs(program, log))
		if len(inputs) == 1 {
			firstWait = inputs[0]
			return true
		}
		return false
	})
	if firstWait.ProcessID() != recovered.ID() || string(firstWait.Prompt()) != `"Approve?"` {
		t.Fatal("recovered question changed its process or prompt")
	}
	if err := recovered.Answer(ctx, firstWait.WaitKey(), true); err != nil {
		t.Fatal(err)
	}
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		inputs := mustValue(PendingToolInputs(program, log))
		if len(inputs) == 1 && inputs[0].WaitKey() != firstWait.WaitKey() {
			secondWait = inputs[0]
			return true
		}
		return false
	})
	if secondWait.ProcessID() != recovered.ID() || string(secondWait.Prompt()) != `"Give a reason."` {
		t.Fatal("continuation question lost its own identity")
	}
	if err := recovered.Answer(ctx, firstWait.WaitKey(), true); err != nil {
		t.Fatal("retry of an acknowledged answer changed outcome", err)
	}
	if err := recovered.Answer(ctx, secondWait.WaitKey(), "reviewed"); err != nil {
		t.Fatal(err)
	}
	output := awaitOutput(t, recovered)
	if initialCalls.Load() != 1 || modelCalls.Load() != 1 || len(output.DirectToolResults) != 1 {
		t.Fatal("question recovery repeated external work or lost the final result")
	}
	if text, textual := output.DirectToolResults[0].Output.Text(); !textual || text != "approved after review" {
		t.Fatal("recovered continuation changed its final output")
	}
	previous := run.EffectID{}
	for range 3 {
		invocation := <-invocations
		if invocation.Reference() != intended || invocation.ProcessID() != recovered.ID() || previous.Valid() && invocation.EffectID().Step <= previous.Step {
			t.Fatal("continuation changed the logical call or reused an effect")
		}
		previous = invocation.EffectID()
	}
	completed := completedLog(t, store, handle.ID())
	if inputs := mustValue(PendingToolInputs(program, completed)); len(inputs) != 0 {
		t.Fatal("completed call retained an answerable question")
	}
	rounds := mustValue(SettledResults(program, completed))
	if len(rounds) != 1 || !rounds[0].Complete() || rounds[0].Entries()[0].Reference != intended {
		t.Fatal("continuations created additional logical calls")
	}
	barrier.stop()
	if _, err := handle.Await(ctx); !errors.Is(err, errToolSettlementCrash) {
		t.Fatal("old writer did not report its lost settlement", err)
	}
}

func TestSteeringWhileModelIsPendingSurvivesRecovery(t *testing.T) {
	store := run.NewMemoryStore()
	original := conformancetest.Runtime(t, run.Config{Store: store, CheckpointInterval: 1})
	dispatched := make(chan struct{})
	var models atomic.Int32
	model := chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		if models.Add(1) == 1 {
			close(dispatched)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if request.Messages[len(request.Messages)-1].Text() != "new guidance" {
			return nil, errors.New("steering was not applied")
		}
		return finalResponse("steered"), nil
	})
	program := compileTest(t, original, Config{Model: model})
	handle := startInteraction(t, original, program)
	select {
	case <-dispatched:
	case <-time.After(interactionTestTimeout):
		t.Fatal("model did not start")
	}
	signal := mustValue(NewSteerSignal(mustValue(run.ParseSignalID("guidance")), chat.NewUserMessage(chat.NewTextPart("new guidance"))))
	if _, err := handle.DeliverSignals(t.Context(), signal); err != nil {
		t.Fatal(err)
	}
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		for _, entry := range log.Entries {
			if step, ok := entry.Record.(run.Stepped); ok && step.Process == handle.ID() && step.Transition.Consumed() == 1 {
				return true
			}
		}
		return false
	})
	if err := original.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	restoredRuntime := conformancetest.Runtime(t, run.Config{Store: store})
	restored := mustValue(restoredRuntime.Recover(t.Context(), program, handle.ID()))
	log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		view, err := log.Inspect()
		if err != nil {
			t.Fatal(err)
		}
		process, _ := view.Process(handle.ID())
		return len(process.PendingEffects) == 1 && process.PendingEffects[0].Outcome.Status() == run.SettlementStatusUnknown
	})
	view := mustValue(log.Inspect())
	process, _ := view.Process(handle.ID())
	pending := process.PendingEffects[0]
	settlement := mustValue(SettleModelResult(program, pending.Request, finalResponse("previous answer")))
	if err := restored.ResolveUnknownEffect(t.Context(), pending.Request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	output := awaitOutput(t, restored)
	if output.ModelResponse.Text() != "steered" || models.Load() != 2 {
		t.Fatal("pending steering did not own the next model request")
	}
}
