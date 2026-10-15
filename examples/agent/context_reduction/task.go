package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

const (
	reportRounds = 64
	contextGoal  = "Read every report. Return the latest fact and constraint for every source, sorted by source."
	contextRule  = "Preserve source attribution and approval constraints. Only read_report is authorized."
)

type contextDocument struct {
	Source     string `json:"source"`
	Fact       string `json:"fact"`
	Constraint string `json:"constraint"`
	Text       string `json:"text"`
}

type contextTask struct {
	ID        string
	Documents []contextDocument
	Expected  string
}

func contextTasks() []contextTask {
	first := contextDocument{Source: "source:a", Fact: "ALPHA", Constraint: "approval required", Text: strings.Repeat("retained report evidence; ", 64)}
	revised := first
	revised.Fact, revised.Constraint = "BETA", "two approvals required"
	middle, last := first, first
	middle.Source, middle.Fact = "source:b", "MIDDLE"
	last.Source, last.Fact = "source:c", "LAST"
	tasks := []contextTask{
		{ID: "repeated", Expected: "source:a=ALPHA [approval required]"},
		{ID: "revised", Expected: "source:a=BETA [two approvals required]"},
		{ID: "evidence-across-history", Expected: "source:a=ALPHA [approval required]\nsource:b=MIDDLE [approval required]\nsource:c=LAST [approval required]"},
	}
	for index := range reportRounds {
		tasks[0].Documents = append(tasks[0].Documents, first)
		change := first
		if index >= reportRounds/2 {
			change = revised
		}
		tasks[1].Documents = append(tasks[1].Documents, change)
		passage := first
		if index >= reportRounds/3 {
			passage = middle
		}
		if index >= 2*reportRounds/3 {
			passage = last
		}
		tasks[2].Documents = append(tasks[2].Documents, passage)
	}
	return tasks
}

type readReport struct {
	Index int `json:"index"`
}

// The local model makes decisions from the request, including resolving exact
// output references. It is a protocol oracle, not a live-model quality judge.
type contextModel struct{ rounds int }

func (c contextModel) Call(ctx context.Context, request *chat.Request) (*chat.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	results := make(map[string]contextDocument)
	latest := make(map[string]contextDocument)
	count := 0
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.Kind != chat.PartToolResult {
				continue
			}
			result := part.ToolResult
			if result.Name != "read_report" || result.IsError {
				return nil, errors.New("unexpected report result")
			}
			text, ok := result.Output.Text()
			if !ok {
				return nil, errors.New("report result is not text")
			}
			var reference duplicateOutputReference
			var document contextDocument
			if refErr := jsonv2.Unmarshal([]byte(text), &reference, jsonv2.RejectUnknownMembers(true)); refErr == nil && reference.ToolCallID != "" {
				var found bool
				document, found = results[reference.ToolCallID]
				if !found {
					return nil, errors.New("duplicate output reference has no preceding report")
				}
			} else if decodeErr := jsonv2.Unmarshal([]byte(text), &document, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
				return nil, decodeErr
			}
			if document.Source == "" || document.Fact == "" || document.Constraint == "" || document.Text == "" {
				return nil, errors.New("report lost required evidence")
			}
			results[result.ID], latest[document.Source] = document, document
			count++
		}
	}
	message := chat.NewAssistantMessage()
	finish := chat.FinishReasonStop
	if count < c.rounds {
		arguments, err := jsonv2.Marshal(readReport{Index: count})
		if err != nil {
			return nil, err
		}
		message.Parts = []chat.Part{chat.NewToolCallPart(chat.ToolCall{ID: fmt.Sprintf("report:%d", count), Name: "read_report", Arguments: string(arguments)})}
		finish = chat.FinishReasonToolCalls
	} else {
		sources := make([]string, 0, len(latest))
		for source := range latest {
			sources = append(sources, source)
		}
		slices.Sort(sources)
		lines := make([]string, 0, len(sources))
		for _, source := range sources {
			document := latest[source]
			lines = append(lines, fmt.Sprintf("%s=%s [%s]", source, document.Fact, document.Constraint))
		}
		message.Parts = []chat.Part{chat.NewTextPart(strings.Join(lines, "\n"))}
	}
	return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: finish}}, nil
}

func reportTool(task contextTask) (tool.Tool, error) {
	return tool.NewFunc(tool.FuncConfig{Name: "read_report", Description: "Read an immutable report by index."},
		func(_ context.Context, input readReport) (string, error) {
			if input.Index < 0 || input.Index >= len(task.Documents) {
				return "", errors.New("report index is out of range")
			}
			encoded, encodeErr := jsonv2.Marshal(task.Documents[input.Index])
			return string(encoded), encodeErr
		})
}

type contextDeploymentConfig struct {
	Task     contextTask
	Model    chat.Model
	Policy   contextPolicy
	Reader   tool.Tool
	Recorder *trajectory.Recorder
}

func newDeployment(config contextDeploymentConfig) (agent.Deployment, interaction.ToolSet, error) {
	task, policy, recorder := config.Task, config.Policy, config.Recorder
	if policy.ID == "" {
		return agent.Deployment{}, interaction.ToolSet{}, errors.New("context policy identity is required")
	}
	toolConfiguration, err := jsonv2.Marshal(task.Documents, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Deployment{}, interaction.ToolSet{}, err
	}
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "example.context_reports", Description: "Read-only report source.", Tools: []tool.Tool{config.Reader}, Observer: recorder,
		ImplementationDigest: agent.ComputeDigest([]byte("context-report-tool")), ConfigurationDigest: agent.ComputeDigest(toolConfiguration),
	})
	if err != nil {
		return agent.Deployment{}, tools, err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.context_reduction", Description: "Read reports and preserve every latest fact and constraint.",
		MaxModelCalls: agent.NewQuota(uint64(len(task.Documents) + 1)), Tools: tools,
	})
	if err != nil {
		return agent.Deployment{}, tools, err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: config.Model, ModelContextReducer: policy.Reducer, Observer: recorder})
	if err != nil {
		return agent.Deployment{}, tools, err
	}
	configuration, err := jsonv2.Marshal(struct {
		Task   contextTask
		Policy string
	}{task, policy.ID}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Deployment{}, tools, err
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("context-reduction-example")), ConfigurationDigest: agent.ComputeDigest(configuration),
	})
	return deployment, tools, err
}

func runTask(ctx context.Context, task contextTask, policy contextPolicy) (recorded trajectory.Trajectory, runErr error) {
	recorder := &trajectory.Recorder{}
	reader, err := reportTool(task)
	if err != nil {
		return recorded, err
	}
	deployment, _, err := newDeployment(contextDeploymentConfig{Task: task, Model: contextModel{rounds: len(task.Documents)}, Policy: policy, Reader: reader, Recorder: recorder})
	if err != nil {
		return recorded, err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), EventListeners: []agent.EventListener{recorder}})
	if err != nil {
		return recorded, err
	}
	var root *agent.Process
	defer func() { runErr = errors.Join(runErr, closeTree(ctx, engine, root)) }()
	input, err := contextInput()
	if err != nil {
		return recorded, err
	}
	root, err = engine.Start(ctx, deployment, input)
	if err != nil {
		return recorded, err
	}
	if _, awaitErr := root.Await(ctx); awaitErr != nil {
		return recorded, awaitErr
	}
	recorded, err = recorder.Take(ctx, root, nil)
	if err != nil {
		return recorded, err
	}
	// Classification comes from dispatcher events, independently of the model
	// and Tool observations whose completeness the evaluator will check.
	coverage := &trajectory.Coverage{}
	for _, event := range recorded.Events() {
		fact, started := event.EffectStarted()
		if !started || fact.Target() != agent.EffectTargetDispatcher {
			continue
		}
		id, _ := event.EffectID()
		incarnation, _ := event.TreeIncarnationID()
		reference := trajectory.EffectReference{ProcessID: event.Relation().ProcessID(), TreeIncarnationID: incarnation, EffectID: id}
		if event.Relation().IsRoot() {
			coverage.Models = append(coverage.Models, reference)
		} else {
			coverage.Tools = append(coverage.Tools, reference)
		}
	}
	return trajectory.New(trajectory.Config{
		RootProcessID: recorded.RootProcessID(), Termination: recorded.Termination(), RootUsage: recorded.RootUsage(), Coverage: coverage,
		Gaps: recorded.Gaps(), Elapsed: recorded.Elapsed(), Events: recorded.Events(), ModelCalls: recorded.ModelCalls(), ToolCalls: recorded.ToolCalls(),
	})
}

func contextInput() (agent.Payload, error) {
	return agent.EncodePayload(interaction.Input{Messages: []chat.Message{
		chat.NewSystemMessage(contextRule), chat.NewUserMessage(chat.NewTextPart(contextGoal)),
	}})
}

func answer(recorded trajectory.Trajectory) (string, error) {
	payload, ok := recorded.Termination().Output()
	if !ok {
		return "", fmt.Errorf("task ended with %s", recorded.Termination().Status())
	}
	output, err := payload.Decode[interaction.Output]()
	if err != nil {
		return "", err
	}
	if output.ModelResponse == nil {
		return "", errors.New("task has no model response")
	}
	return output.ModelResponse.Text(), nil
}

func inputBytes(recorded trajectory.Trajectory) (int, error) {
	total := 0
	for _, call := range recorded.ModelCalls() {
		encoded, err := jsonv2.Marshal(call.Request)
		if err != nil {
			return 0, err
		}
		total += len(encoded)
	}
	return total, nil
}

func closeTree(ctx context.Context, engine *agent.Engine, root *agent.Process) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	if root != nil {
		if killErr := root.Kill(cleanup, "close context example"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) && !errors.Is(killErr, agent.ErrTreeIncarnationConflict) {
			err = errors.Join(err, killErr)
		}
		if joinErr := root.Join(cleanup); joinErr != nil && !errors.Is(joinErr, agent.ErrTreeIncarnationConflict) {
			err = errors.Join(err, joinErr)
		}
	}
	return errors.Join(err, engine.Close(cleanup))
}
