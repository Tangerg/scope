package main

import (
	"bytes"
	"context"
	startrand "crypto/rand"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/planning/goap"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "objective: ship agent\n" +
		"workers: 3\n" +
		"summary: synthesized 3 ordered worker results\n" +
		"processes: 6\n"
	if output.String() != want {
		t.Fatalf("output=%q, want %q", output.String(), want)
	}
}

func TestInteractionCanDelegateExactPlanningWorkers(t *testing.T) {
	store := agentrun.NewMemoryStore()
	runtime := conformancetest.Runtime(t, agentrun.Config{Store: store})
	planningWorker, taskState := newPlanningWorker(t, runtime.Compiler())
	model := &planningDelegateModel{}
	root := newPlanningDelegateRoot(t, runtime.Compiler(), planningWorker, model)
	result := runPlanningDelegate(t, runtime, root)
	assertPlanningDelegateResult(t, result, model, taskState)
	assertPlanningDelegateTree(t, store, result.ProcessID(), planningWorker.ProgramRef())
}

func newPlanningDelegateRoot(
	t *testing.T,
	compiler *agentrun.Compiler,
	planningWorker agentrun.Program[agentrun.Payload, agentrun.Payload],
	model *planningDelegateModel,
) agentrun.Program[agentrun.Payload, agentrun.Payload] {
	t.Helper()
	budget := agentrun.Budget{Steps: agentrun.NewQuota(32), Effects: agentrun.NewQuota(32), Messages: agentrun.NewQuota(64)}
	delegate, err := interaction.NewDelegate(interaction.DelegateConfig{
		Name:        "review_with_planning",
		Description: "Use goal-directed planning to complete one review task.",
		Child:       agentrun.ChildBinding{Program: planningWorker, Budget: budget},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := chatclient.New(model)
	if err != nil {
		t.Fatal(err)
	}
	program, err := interaction.Compile(compiler, interaction.Config{
		Name: "example.orchestrator_workers.planning_delegate", Description: "Delegate model-selected tasks to exact Planning workers.",
		Revision: "planning-delegate-v1", Model: interaction.CallModel(client), MaxModelCalls: agentrun.NewQuota(2), Delegates: []interaction.Delegate{delegate}, CompletionValidator: validatePlanningCompletion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return program.Erase()
}

func validatePlanningCompletion(_ context.Context, candidate interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
	artifacts := candidate.Artifacts()
	if len(artifacts) != 2 {
		return interaction.CompletionDecision{Feedback: "Complete both planned review tasks."}, nil
	}
	for index, artifact := range artifacts {
		output, err := artifact.Output().Decode[planning.Output]()
		if err != nil {
			return interaction.CompletionDecision{}, err
		}
		if artifact.DelegateName() != "review_with_planning" || output.Outcome != planning.OutcomeAchieved {
			return interaction.CompletionDecision{}, fmt.Errorf("planning artifact %d was not achieved", index)
		}
	}
	return interaction.CompletionDecision{Accepted: true}, nil
}

func runPlanningDelegate(t *testing.T, runtime *agentrun.Runtime, root agentrun.Program[agentrun.Payload, agentrun.Payload]) agentrun.Result[agentrun.Payload] {
	t.Helper()
	input, err := agentrun.EncodePayload(interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("plan both reviews, then synthesize")),
	}})
	if err != nil {
		t.Fatal(err)
	}
	process, err := runtime.Start(context.Background(), root, agentrun.StartRequest[agentrun.Payload]{ID: agentrun.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertPlanningDelegateResult(
	t *testing.T,
	result agentrun.Result[agentrun.Payload],
	model *planningDelegateModel,
	taskState *planningTaskState,
) {
	t.Helper()
	payload, err := result.Output()
	if err != nil {
		t.Fatal(err)
	}
	output, err := payload.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.ModelResponse == nil || output.ModelResponse.Text() != "planning workers achieved 2 tasks" ||
		model.Calls() != 2 || taskState.CompletedCount() != 2 {
		t.Fatalf("output=%#v model calls=%d completed=%d", output, model.Calls(), taskState.CompletedCount())
	}
}

func assertPlanningDelegateTree(
	t *testing.T,
	store *agentrun.MemoryStore,
	processID agentrun.ProcessID,
	planningRef agentrun.ProgramRef,
) {
	t.Helper()
	log, found, err := store.Load(t.Context(), processID)
	if err != nil || !found {
		t.Fatal(err)
	}
	starts, planningChildren := 0, 0
	for _, entry := range log.Entries {
		if started, ok := entry.Record.(agentrun.Started); ok {
			starts++
			if started.Program == planningRef {
				planningChildren++
			}
		}
	}
	if starts != 3 || planningChildren != 2 {
		t.Fatalf("started=%d planning children=%d", starts, planningChildren)
	}
}

type planningTaskState struct {
	incomplete planning.Condition
	complete   planning.Condition

	mu        sync.Mutex
	completed map[string]bool
}

func (p *planningTaskState) CompletedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.completed)
}

func (p *planningTaskState) sense(_ context.Context, request planning.SenseRequest) (planning.WorldState, error) {
	task, err := request.Input.Decode[workerTask]()
	if err != nil {
		return planning.WorldState{}, err
	}
	p.mu.Lock()
	done := p.completed[task.ID]
	p.mu.Unlock()
	if done {
		return planning.NewWorldState(p.complete)
	}
	return planning.NewWorldState(p.incomplete)
}

func (p *planningTaskState) review(_ context.Context, request planning.ActionRequest) (planning.ActionResult, error) {
	if request.ActionName != "review" {
		return planning.ActionResult{}, errors.New("unexpected planning action")
	}
	task, err := request.Input.Decode[workerTask]()
	if err != nil {
		return planning.ActionResult{}, err
	}
	p.mu.Lock()
	p.completed[task.ID] = true
	p.mu.Unlock()
	return planning.ActionSucceeded(), nil
}

func newPlanningWorker(t *testing.T, compiler *agentrun.Compiler) (agentrun.Program[agentrun.Payload, agentrun.Payload], *planningTaskState) {
	t.Helper()
	incomplete, err := planning.NewCondition("review.complete", planning.TruthFalse)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := planning.NewCondition("review.complete", planning.TruthTrue)
	if err != nil {
		t.Fatal(err)
	}
	action, err := planning.NewAction(planning.ActionConfig{
		Name: "review", Description: "Complete the requested review task.",
		Preconditions: []planning.Condition{incomplete}, Effects: []planning.Condition{complete},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &planningTaskState{incomplete: incomplete, complete: complete, completed: make(map[string]bool)}
	binding, err := planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: action, Executor: planning.ActionExecutorFunc(state.review)})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name: "review_complete", Description: "The requested review task is complete.",
		Conditions: []planning.Condition{complete},
	})
	if err != nil {
		t.Fatal(err)
	}
	program, err := planning.Compile[workerTask](compiler, planning.Config{
		Name: "example.orchestrator_workers.planning_worker", Description: "Use GOAP to complete one model-selected review task.",
		Revision: "planning-review-v1", Goal: goal, Actions: []planning.ActionBinding{binding}, Planner: goap.New(goap.Config{}),
		MaxActionAttempts: agentrun.NewQuota(1), Sensor: planning.SensorFunc(state.sense),
	})
	if err != nil {
		t.Fatal(err)
	}
	return program.Erase(), state
}

type planningDelegateModel struct {
	mu    sync.Mutex
	calls int
}

func (p *planningDelegateModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return delegatePlanningTasks(request)
	}
	return summarizePlanningResults(request)
}

func delegatePlanningTasks(request *chat.Request) (*chat.Response, error) {
	if len(request.Tools) != 1 || request.Tools[0].Name != "review_with_planning" {
		return nil, fmt.Errorf("planning Delegate manifest=%#v", request.Tools)
	}
	first, err := jsonv2.Marshal(workerTask{
		ID: "facts", Instruction: "review facts",
	})
	if err != nil {
		return nil, fmt.Errorf("encode first Planning task: %w", err)
	}
	second, err := jsonv2.Marshal(workerTask{
		ID: "risks", Instruction: "review risks",
	})
	if err != nil {
		return nil, fmt.Errorf("encode second Planning task: %w", err)
	}
	message := chat.NewAssistantMessage(
		chat.NewToolCallPart(chat.ToolCall{
			ID: "call_planning_facts", Name: "review_with_planning", Arguments: string(first),
		}),
		chat.NewToolCallPart(chat.ToolCall{
			ID: "call_planning_risks", Name: "review_with_planning", Arguments: string(second),
		}),
	)
	return chat.NewResponse(&chat.Output{
		Message: &message, FinishReason: chat.FinishReasonToolCalls,
	}, nil)
}

func summarizePlanningResults(request *chat.Request) (*chat.Response, error) {
	if len(request.Messages) != 3 || request.Messages[2].Role != chat.RoleTool ||
		len(request.Messages[2].Parts) != 2 {
		return nil, fmt.Errorf("planning results context=%#v", request.Messages)
	}
	for index, part := range request.Messages[2].Parts {
		if part.ToolResult == nil || part.ToolResult.IsError {
			return nil, fmt.Errorf("planning tool result %d=%#v", index, part.ToolResult)
		}
		var output planning.Output
		if err := jsonv2.Unmarshal(part.ToolResult.Output.Details, &output); err != nil {
			return nil, fmt.Errorf("decode planning tool result %d: %w", index, err)
		}
		if output.Outcome != planning.OutcomeAchieved {
			return nil, fmt.Errorf("planning tool result %d did not achieve goal: %#v", index, output)
		}
	}
	message := chat.NewAssistantMessage(chat.NewTextPart("planning workers achieved 2 tasks"))
	return chat.NewResponse(&chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}, nil)
}

func (p *planningDelegateModel) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestMainEntryPointRunsToCompletion(t *testing.T) {
	// main wires the same in-process example as run; exercising it keeps the
	// command entry point covered. run uses only in-process fakes, so it cannot
	// reach the os.Exit failure branch.
	main()
}

func TestOrchestrationRefusesAnUnusablePlanAndPartialWorkerEvidence(t *testing.T) {
	ctx := t.Context()
	for _, mode := range []string{"no model reply", "empty tasks", "malformed plan"} {
		t.Run(mode, func(t *testing.T) {
			var out interaction.Output
			switch mode {
			case "empty tasks":
				r, e := jsonResponse(workPlan{Tasks: []workerTask{}})
				if e != nil {
					t.Fatal(e)
				}
				out.ModelResponse = r
			case "malformed plan":
				msg := chat.NewAssistantMessage(chat.NewTextPart("this is not JSON"))
				r, e := chat.NewResponse(&chat.Output{Message: &msg, FinishReason: chat.FinishReasonStop}, nil)
				if e != nil {
					t.Fatal(e)
				}
				out.ModelResponse = r
			}
			if tasks, err := parseWorkPlan(ctx, out); len(tasks) != 0 || err == nil {
				t.Fatalf("published tasks=%v err=%v", tasks, err)
			}
		})
	}
	for _, wire := range []string{`[]`, `[{"finding":"first"},{"finding":""}]`, `invalid`} {
		r, e := (synthesisModel{}).Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(wire))}})
		if r != nil || e == nil {
			t.Fatalf("produced report for %s: %v %v", wire, r, e)
		}
	}
	r, err := (decompositionModel{}).Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(`{"objective":""}`))}})
	if r != nil || err == nil {
		t.Fatal("empty objective acquired a work plan", err)
	}
	if result, err := executeWorkerTask(ctx, workerTask{ID: "facts"}); result.Finding != "" || err == nil {
		t.Fatal("published an incomplete task finding", result, err)
	}
}

func TestMissingStageBindingsNeverPublishPartialOrchestration(t *testing.T) {
	c, err := newOrchestratorChildren(conformancetest.Compiler(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"decomposer", "worker", "synthesizer"} {
		bad := c
		var zero agentrun.Program[agentrun.Payload, agentrun.Payload]
		switch role {
		case "decomposer":
			bad.decomposer = zero
		case "worker":
			bad.worker = zero
		case "synthesizer":
			bad.synthesizer = zero
		}
		if stages, err := bad.stages(); len(stages) != 0 || err == nil {
			t.Fatalf("partial stages with missing %s: %v %v", role, stages, err)
		}
	}
}
