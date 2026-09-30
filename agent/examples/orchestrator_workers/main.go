// Command orchestrator_workers demonstrates model-directed task decomposition,
// deterministic managed worker fan-out, and model synthesis without a
// collaboration Strategy. It uses local deterministic models and
// requires no network access.
package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

const (
	plannedWorkerCount  = 3
	workerBudgetSteps   = 32
	workerBudgetEffects = 32
	workerBudgetSignals = 64
	workerParallelism   = 2
	maximumPlannedTasks = 8
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	root, resolver, err := newOrchestratorWorkers()
	if err != nil {
		return err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), DeploymentResolver: resolver})
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, engine.Close(context.WithoutCancel(ctx)))
	}()

	input, err := agent.EncodePayload(orchestrationGoal{Objective: "ship agent"})
	if err != nil {
		return err
	}
	process, err := engine.Start(ctx, root, input)
	if err != nil {
		return err
	}
	result, err := process.Await(ctx)
	if err != nil {
		return err
	}
	report, err := decodeCompleted[orchestrationReport](result)
	if err != nil {
		return err
	}
	tree, err := engine.CaptureTree(ctx, process.ID())
	if err != nil {
		return err
	}
	if len(report.Results) != plannedWorkerCount {
		return fmt.Errorf("orchestration returned %d worker results", len(report.Results))
	}
	_, err = fmt.Fprintf(
		output,
		"objective: %s\nworkers: %s, %s, %s\nsummary: %s\nprocesses: %d\n",
		report.Objective,
		report.Results[0].TaskID,
		report.Results[1].TaskID,
		report.Results[2].TaskID,
		report.Summary,
		len(tree.ProcessSnapshots()),
	)
	return err
}

type orchestrationGoal struct {
	Objective string `json:"objective"`
}

type workerTask struct {
	ID          string `json:"id"`
	Objective   string `json:"objective"`
	Instruction string `json:"instruction"`
}

type workPlan struct {
	Tasks []workerTask `json:"tasks"`
}

type workerResult struct {
	TaskID    string `json:"task_id"`
	Objective string `json:"objective"`
	Finding   string `json:"finding"`
}

type orchestrationReport struct {
	Objective string         `json:"objective"`
	Summary   string         `json:"summary"`
	Results   []workerResult `json:"results"`
}

func newOrchestratorWorkers() (agent.Deployment, deploymentResolver, error) {
	children, err := newOrchestratorChildren()
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	stages, err := children.stages()
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.orchestrator_workers", Description: "Decompose, execute, and synthesize with managed child Processes.",
		Stages: stages,
	})
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	root, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte("example-orchestrator-workers-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-orchestrator-workers-configuration")),
	})
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	return root, deploymentResolver{
		children.decomposer.DeploymentRef():  children.decomposer,
		children.worker.DeploymentRef():      children.worker,
		children.synthesizer.DeploymentRef(): children.synthesizer,
	}, nil
}

type orchestratorChildren struct {
	decomposer  agent.Deployment
	worker      agent.Deployment
	synthesizer agent.Deployment
}

func newOrchestratorChildren() (orchestratorChildren, error) {
	decomposer, err := interactionDeployment(
		"example.orchestrator_workers.decomposer",
		"Decompose one objective into a bounded ordered task list.",
		decompositionModel{},
	)
	if err != nil {
		return orchestratorChildren{}, err
	}
	worker, err := transformDeployment(
		"example.orchestrator_workers.worker",
		"Execute one decomposed task and return a typed finding.",
		executeWorkerTask,
	)
	if err != nil {
		return orchestratorChildren{}, err
	}
	synthesizer, err := interactionDeployment(
		"example.orchestrator_workers.synthesizer",
		"Synthesize ordered typed worker results into a final report.",
		synthesisModel{},
	)
	if err != nil {
		return orchestratorChildren{}, err
	}
	return orchestratorChildren{decomposer: decomposer, worker: worker, synthesizer: synthesizer}, nil
}

func (o orchestratorChildren) stages() ([]workflow.Stage, error) {
	budget := agent.Budget{
		Steps: agent.NewQuota(workerBudgetSteps), Effects: agent.NewQuota(workerBudgetEffects), Signals: agent.NewQuota(workerBudgetSignals),
	}
	renderGoal, err := workflow.Transform("render_goal", interactionInput[orchestrationGoal])
	if err != nil {
		return nil, err
	}
	decompose, err := workflow.Call(workflow.CallConfig{
		ID: "decompose", Deployment: o.decomposer, Budget: budget,
	})
	if err != nil {
		return nil, err
	}
	parsePlan, err := workflow.Transform("parse_plan", parseWorkPlan)
	if err != nil {
		return nil, err
	}
	execute, err := workflow.Map(workflow.MapConfig[workerTask, workerResult]{
		ID: "execute", Deployment: o.worker, Budget: budget,
		WindowSize: workerParallelism, MaxItems: maximumPlannedTasks,
	})
	if err != nil {
		return nil, err
	}
	renderResults, err := workflow.Transform("render_results", interactionInput[[]workerResult])
	if err != nil {
		return nil, err
	}
	synthesize, err := workflow.Call(workflow.CallConfig{
		ID: "synthesize", Deployment: o.synthesizer, Budget: budget,
	})
	if err != nil {
		return nil, err
	}
	parseReport, err := workflow.Transform("parse_report", decodeModelJSON[orchestrationReport])
	if err != nil {
		return nil, err
	}
	return []workflow.Stage{renderGoal, decompose, parsePlan, execute, renderResults, synthesize, parseReport}, nil
}

func executeWorkerTask(_ context.Context, task workerTask) (workerResult, error) {
	if task.ID == "" || task.Objective == "" || task.Instruction == "" {
		return workerResult{}, errors.New("worker task is incomplete")
	}
	return workerResult{
		TaskID: task.ID, Objective: task.Objective,
		Finding: task.Instruction + ": complete",
	}, nil
}

func parseWorkPlan(ctx context.Context, output interaction.Output) ([]workerTask, error) {
	plan, err := decodeModelJSON[workPlan](ctx, output)
	if err != nil {
		return nil, err
	}
	if len(plan.Tasks) == 0 {
		return nil, errors.New("decomposer returned no tasks")
	}
	return plan.Tasks, nil
}

func interactionDeployment(name, description string, model chat.Model) (agent.Deployment, error) {
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		return agent.Deployment{}, err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: name, Description: description, MaxModelCalls: agent.NewQuota(1),
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client})
	if err != nil {
		return agent.Deployment{}, err
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte(name + "-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(name + "-configuration")),
	})
}

func transformDeployment[I, O any](
	name string,
	description string,
	transform workflow.TransformFunc[I, O],
) (agent.Deployment, error) {
	stage, err := workflow.Transform("execute", transform)
	if err != nil {
		return agent.Deployment{}, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: name, Description: description, Stages: []workflow.Stage{stage},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(name + "-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(name + "-configuration")),
	})
}

func interactionInput[T any](_ context.Context, value T) (interaction.Input, error) {
	data, err := jsonv2.Marshal(value)
	if err != nil {
		return interaction.Input{}, err
	}
	return interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(data)))},
	}, nil
}

func decodeModelJSON[T any](_ context.Context, output interaction.Output) (T, error) {
	var zero T
	if output.Source != interaction.CompletionSourceModelResponse || output.ModelResponse == nil {
		return zero, errors.New("interaction did not return a model response")
	}
	if err := jsonv2.Unmarshal([]byte(output.ModelResponse.Text()), &zero, jsonv2.RejectUnknownMembers(true)); err != nil {
		return zero, fmt.Errorf("decode model JSON: %w", err)
	}
	return zero, nil
}

func decodeCompleted[T any](result agent.Result) (T, error) {
	var zero T
	if result.Status() != agent.StatusCompleted {
		return zero, fmt.Errorf("process ended with %s: %#v", result.Status(), result.Termination())
	}
	output, present := result.Output()
	if !present {
		return zero, errors.New("completed Process has no Output")
	}
	return output.Decode[T]()
}

type decompositionModel struct{}

func (decompositionModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	var goal orchestrationGoal
	if err := jsonv2.Unmarshal([]byte(request.Messages[0].Text()), &goal); err != nil || goal.Objective == "" {
		return nil, errors.New("decomposition model received an invalid objective")
	}
	plan := workPlan{Tasks: []workerTask{
		{ID: "facts", Objective: goal.Objective, Instruction: "collect facts for " + goal.Objective},
		{ID: "risks", Objective: goal.Objective, Instruction: "identify risks for " + goal.Objective},
		{ID: "recommendation", Objective: goal.Objective, Instruction: "recommend next steps for " + goal.Objective},
	}}
	return jsonResponse(plan)
}

type synthesisModel struct{}

func (synthesisModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	var results []workerResult
	if err := jsonv2.Unmarshal([]byte(request.Messages[0].Text()), &results); err != nil || len(results) == 0 {
		return nil, errors.New("synthesis model received invalid worker results")
	}
	objective := results[0].Objective
	for index, result := range results {
		if result.TaskID == "" || result.Objective != objective || result.Finding == "" {
			return nil, fmt.Errorf("worker result %d is incomplete", index)
		}
	}
	return jsonResponse(orchestrationReport{
		Objective: objective,
		Summary:   fmt.Sprintf("synthesized %d ordered worker results", len(results)),
		Results:   results,
	})
}

func jsonResponse(value any) (*chat.Response, error) {
	data, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, err
	}
	message := chat.NewAssistantMessage(chat.NewTextPart(string(data)))
	return chat.NewResponse(&chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}, nil)
}

type deploymentResolver map[agent.DeploymentRef]agent.Deployment

func (d deploymentResolver) Resolve(
	reference agent.DeploymentRef,
) (agent.Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return agent.Deployment{}, fmt.Errorf("deployment %s is not registered", reference.Digest())
	}
	return deployment, nil
}
