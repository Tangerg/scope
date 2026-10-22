package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

const lostToolRound uint64 = 17

type confirmedToolCall struct {
	EffectID agent.EffectID
	Output   chat.ToolOutput
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
	invocation, ok := interaction.ToolInvocationFromContext(ctx)
	if !ok {
		return tool.Result{}, errors.New("missing Tool invocation")
	}
	reference, ok := invocation.Reference()
	if !ok {
		return tool.Result{}, errors.New("missing Tool parent reference")
	}
	if reference.ModelCallSequence() == lostToolRound {
		select {
		case l.confirmed <- confirmedToolCall{EffectID: invocation.EffectID(), Output: output.Output.Clone()}:
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

func (c *contextProbe) ReduceModelContext(ctx context.Context, invocation interaction.ModelInvocation, request *chat.Request) ([]chat.Message, error) {
	messages, err := (duplicateToolOutputReducer{}).ReduceModelContext(ctx, invocation, request)
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
	ctx := t.Context()
	task := contextTasks()[2]
	confirmed := make(chan confirmedToolCall, 1)
	before := &trajectory.Recorder{}
	reader, err := reportTool(task)
	if err != nil {
		t.Fatal(err)
	}
	originalProbe := &contextProbe{}
	deployment, _, err := newDeployment(contextDeploymentConfig{
		Task: task, Model: contextModel{rounds: len(task.Documents)}, Policy: contextPolicy{ID: duplicateOutputPolicyID, Reducer: originalProbe},
		Reader: losingReportTool{Tool: reader, confirmed: confirmed}, Recorder: before,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := agent.NewMemoryTreeCommitter()
	observations := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, EventListeners: []agent.EventListener{before, observations}})
	if err != nil {
		t.Fatal(err)
	}
	var root *agent.Process
	t.Cleanup(func() {
		if closeErr := closeTree(context.WithoutCancel(ctx), engine, root); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := contextInput()
	if err != nil {
		t.Fatal(err)
	}
	root, err = engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, waitErr := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		fact, ok := event.EffectFinished()
		return ok && !event.Relation().IsRoot() && fact.SettlementStatus() == agent.SettlementStatusUnknown
	}); waitErr != nil {
		t.Fatal(waitErr)
	}
	var receipt confirmedToolCall
	select {
	case receipt = <-confirmed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if pauseErr := root.Pause(ctx, "restore committed context while the Tool is unknown"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	if _, waitErr := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		return event.Name() == agent.EventProcessPaused && event.Relation().IsRoot()
	}); waitErr != nil {
		t.Fatal(waitErr)
	}
	effectiveBefore := originalProbe.LastEffective()
	if _, captureErr := engine.CaptureTree(ctx, root.Relation().ProcessID()); captureErr != nil {
		t.Fatal(captureErr)
	}
	head, found, err := store.LoadTree(ctx, root.Relation().ProcessID())
	if err != nil || !found {
		t.Fatalf("acknowledged head: found=%t, error=%v", found, err)
	}
	snapshot, err := agent.ParseTreeSnapshot(head.JSON())
	if err != nil {
		t.Fatal(err)
	}
	var request agent.EffectRequest
	for _, process := range snapshot.ProcessSnapshots() {
		for _, id := range process.UnknownEffectIDs() {
			if request.Valid() {
				t.Fatal("more than one unknown operation")
			}
			request, found = snapshot.EffectRequest(process.Relation().ProcessID(), id)
			if !found {
				t.Fatal("unknown operation lost its request")
			}
		}
	}
	if !request.Valid() || request.ID() != receipt.EffectID {
		t.Fatal("confirmed Tool response lost its frozen request identity")
	}
	after := &trajectory.Recorder{}
	probe := &contextProbe{}
	replacementDeployment, tools, err := newDeployment(contextDeploymentConfig{
		Task: task, Model: contextModel{rounds: len(task.Documents)}, Policy: contextPolicy{ID: duplicateOutputPolicyID, Reducer: probe}, Reader: reader, Recorder: after,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacementDeployment.DeploymentRef() != deployment.DeploymentRef() {
		t.Fatal("instrumentation changed the deployment binding")
	}
	replacement, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, EventListeners: []agent.EventListener{after}})
	if err != nil {
		t.Fatal(err)
	}
	var restored *agent.Process
	t.Cleanup(func() {
		if closeErr := closeTree(context.WithoutCancel(ctx), replacement, restored); closeErr != nil {
			t.Error(closeErr)
		}
	})
	restored, err = replacement.RestoreTree(ctx, replacementDeployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// Tool settlement carries no replacement model context: the only source
	// for the recovered prefix is the context already committed in the tree.
	settlement, err := tools.SettleToolResult(request, receipt.Output, interaction.ResultSucceeded, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, found := replacement.Process(request.Relation().ProcessID())
	if !found {
		t.Fatal("restored Tool owner is missing")
	}
	if resumeErr := restored.Resume(ctx); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	if resolveErr := owner.ResolveUnknownEffect(ctx, request.ID(), settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if _, awaitErr := restored.Await(ctx); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	recorded, err := after.Take(ctx, restored, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, err := answer(recorded)
	if err != nil || text != task.Expected {
		t.Fatalf("restoration degraded task quality: %q, %v", text, err)
	}
	requests := probe.Requests()
	if len(originalProbe.Requests()) != int(lostToolRound) || len(requests) != reportRounds+1-int(lostToolRound) || len(recorded.ModelCalls()) != len(requests) || recorded.ModelCalls()[0].CallSequence != lostToolRound+1 || len(recorded.ToolCalls()) != reportRounds-int(lostToolRound) {
		t.Fatalf("restoration replayed model work: requests=%d, calls=%+v", len(requests), recorded.ModelCalls())
	}
	for _, effective := range requests {
		// Observe before reduction. A reducer running again must not conceal
		// a recovered WorkingContext that regrew discarded duplicate bodies.
		if len(effective.Messages) < len(effectiveBefore.Messages) || !reflect.DeepEqual(effective.Messages[:len(effectiveBefore.Messages)], effectiveBefore.Messages) {
			t.Fatal("pre-reduction restored context regrew or lost committed evidence")
		}
		if !reflect.DeepEqual(effective.Tools, effectiveBefore.Tools) || !reflect.DeepEqual(effective.ToolChoice, effectiveBefore.ToolChoice) || !reflect.DeepEqual(effective.Options, effectiveBefore.Options) {
			t.Fatal("restoration changed Tool authority or model options")
		}
	}
}
