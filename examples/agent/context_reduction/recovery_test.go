package main

import (
	"context"
	startrand "crypto/rand"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

const (
	lostToolRound        uint64 = 17
	recoveryTestTimeout         = 2 * time.Minute
	recoveryPollInterval        = 10 * time.Millisecond
)

type confirmedToolCall struct {
	EffectID agentrun.EffectID
	Output   tool.Output
}

type losingReportTool struct {
	tool.Tool
	confirmed chan confirmedToolCall
}

func (l losingReportTool) Call(ctx context.Context, input tool.Invocation) (tool.Result, error) {
	output, err := l.Tool.Call(ctx, input)
	if err != nil {
		return tool.Result{}, err
	}
	effectID, err := agentrun.ParseEffectID(input.IdempotencyKey())
	if err != nil {
		return tool.Result{}, err
	}
	var arguments readReport
	if err := jsonv2.Unmarshal(input.Arguments(), &arguments, jsonv2.RejectUnknownMembers(true)); err != nil {
		return tool.Result{}, err
	}
	if uint64(arguments.Index+1) == lostToolRound {
		select {
		case l.confirmed <- confirmedToolCall{EffectID: effectID, Output: output.Output.Clone()}:
			return tool.Result{}, errors.New("confirmed report response was lost")
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}
	return output, nil
}

type contextProbe struct {
	mu       sync.Mutex
	requests []*chat.Request
	last     *chat.Request
}

func (c *contextProbe) PrepareContext(ctx context.Context, invocation interaction.ContextInvocation, request *chat.Request) ([]chat.Message, error) {
	messages, err := (duplicateToolOutputReducer{}).PrepareContext(ctx, invocation, request)
	if err != nil {
		return nil, err
	}
	effective := request.Clone()
	effective.Messages = messages
	c.mu.Lock()
	c.requests = append(c.requests, request.Clone())
	c.last = effective.Clone()
	c.mu.Unlock()
	return messages, nil
}

func (c *contextProbe) LastEffective() *chat.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last.Clone()
}

func (c *contextProbe) Requests() []*chat.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]*chat.Request, len(c.requests))
	for index, request := range c.requests {
		requests[index] = request.Clone()
	}
	return requests
}

func TestRestoredContextDoesNotRegrowOrChangeAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), recoveryTestTimeout)
	defer cancel()
	task := contextTasks()[2]
	confirmed := make(chan confirmedToolCall, 1)
	reader, err := reportTool(task)
	if err != nil {
		t.Fatal(err)
	}
	store := agentrun.NewMemoryStore()
	runtime, err := agentrun.New(ctx, agentrun.Config{Store: store, Build: contextBuild})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := closeRuntime(ctx, runtime); closeErr != nil {
			t.Error(closeErr)
		}
	})
	originalProbe := &contextProbe{}
	program, err := compileContext(runtime.Compiler(), contextProgramConfig{Task: task, Model: contextModel{rounds: len(task.Documents)}, Policy: contextPolicy{ID: duplicateOutputPolicyID, Reducer: originalProbe}, Reader: losingReportTool{Tool: reader, confirmed: confirmed}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := runtime.Start(ctx, program, agentrun.StartRequest[interaction.Input]{ID: agentrun.StartID{Scope: "test", Key: startrand.Text()}, Input: contextInput()})
	if err != nil {
		t.Fatal(err)
	}
	var receipt confirmedToolCall
	select {
	case receipt = <-confirmed:
	case <-ctx.Done():
		t.Fatal("confirmed tool result missing", ctx.Err())
	}
	awaitRecoveryState(t, ctx, runtime, root.ID(), "the confirmed tool outcome to become unknown", func(view agentrun.TreeInspection) bool {
		process, ok := view.Process(root.ID())
		if !ok {
			return false
		}
		for _, pending := range process.PendingEffects {
			if pending.Request.ID() == receipt.EffectID && pending.Outcome.Status() == agentrun.SettlementStatusUnknown {
				return true
			}
		}
		return false
	})
	if pauseErr := root.Pause(ctx, "restore committed context while the tool is unknown"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	effectiveBefore := originalProbe.LastEffective()
	head, found, err := store.Load(ctx, root.ID())
	if err != nil || !found {
		t.Fatalf("acknowledged log found=%t error=%v", found, err)
	}
	encoded, err := jsonv2.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	var retained agentrun.Log
	if decodeErr := jsonv2.Unmarshal(encoded, &retained, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	before, err := retained.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := before.Process(root.ID())
	if len(initial.PendingEffects) != 1 || initial.PendingEffects[0].Request.ID() != receipt.EffectID {
		t.Fatal("confirmed outcome lost its frozen request")
	}
	if closeErr := runtime.Close(ctx); closeErr != nil {
		t.Fatal(closeErr)
	}
	replacement, err := agentrun.New(ctx, agentrun.Config{Store: store, Build: contextBuild})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := closeRuntime(ctx, replacement); closeErr != nil {
			t.Error(closeErr)
		}
	})
	probe := &contextProbe{}
	recoveredProgram, err := compileContext(replacement.Compiler(), contextProgramConfig{Task: task, Model: contextModel{rounds: len(task.Documents)}, Policy: contextPolicy{ID: duplicateOutputPolicyID, Reducer: probe}, Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	if recoveredProgram.ProgramRef() != program.ProgramRef() {
		t.Fatal("instrumentation changed the program binding")
	}
	restored, err := replacement.Recover(ctx, recoveredProgram, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	view, err := restored.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	process, _ := view.Process(root.ID())
	if len(process.PendingEffects) != 1 {
		t.Fatal("unknown effect disappeared at recovery")
	}
	request := process.PendingEffects[0].Request
	settlement, err := interaction.SettleToolResult(recoveredProgram, request, tool.Result{Output: receipt.Output}, interaction.ResultSucceeded, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolveErr := restored.ResolveUnknownEffect(ctx, request.ID(), settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if resumeErr := restored.Resume(ctx); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	if _, awaitErr := restored.Await(ctx); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	recorded, err := captureContext(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	text, err := answer(recorded)
	if err != nil || text != task.Expected {
		t.Fatalf("recovery degraded quality: %q %v", text, err)
	}
	requests := probe.Requests()
	if len(originalProbe.Requests()) != int(lostToolRound) || len(requests) != reportRounds+1-int(lostToolRound) || len(recorded.ModelCalls()) != reportRounds+1 || len(recorded.ToolCalls()) != reportRounds {
		t.Fatalf("recovery replayed work or lost history: before=%d after=%d model=%d tool=%d", len(originalProbe.Requests()), len(requests), len(recorded.ModelCalls()), len(recorded.ToolCalls()))
	}
	if !recorded.HistoryComplete() {
		t.Fatal("recovery fragmented retained history")
	}
	for _, effective := range requests {
		if len(effective.Messages) < len(effectiveBefore.Messages) || !reflect.DeepEqual(effective.Messages[:len(effectiveBefore.Messages)], effectiveBefore.Messages) {
			t.Fatal("pre-reduction recovered context regrew or lost committed evidence")
		}
		if !reflect.DeepEqual(effective.Tools, effectiveBefore.Tools) || !reflect.DeepEqual(effective.ToolChoice, effectiveBefore.ToolChoice) || !reflect.DeepEqual(effective.Options, effectiveBefore.Options) {
			t.Fatal("recovery changed tool authority or model options")
		}
	}
}

func awaitRecoveryState(t *testing.T, ctx context.Context, runtime *agentrun.Runtime, root agentrun.ProcessID, phase string, ready func(agentrun.TreeInspection) bool) {
	t.Helper()
	ticker := time.NewTicker(recoveryPollInterval)
	defer ticker.Stop()
	for {
		view, err := runtime.Inspect(ctx, root)
		if err != nil {
			t.Fatalf("waiting for %s: %v", phase, err)
		}
		if ready(view) {
			return
		}
		if process, ok := view.Process(root); ok && process.Termination.Valid() {
			t.Fatalf("waiting for %s: root ended with %v", phase, process.Termination)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", phase, ctx.Err())
		case <-ticker.C:
		}
	}
}
