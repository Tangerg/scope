package main

import (
	startrand "crypto/rand"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

const (
	reportRounds       = 64
	readReportToolName = "read_report"
	treeCleanupTimeout = 5 * time.Second
	contextBuild       = "context-reduction-example"
	contextGoal        = "Read every report. Return the latest fact and constraint for every source, sorted by source."
	contextRule        = "Preserve source attribution and approval constraints. Only " + readReportToolName + " is authorized."
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
			if result.Name != readReportToolName || result.IsError {
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
		message.Parts = []chat.Part{chat.NewToolCallPart(chat.ToolCall{ID: fmt.Sprintf("report:%d", count), Name: readReportToolName, Arguments: string(arguments)})}
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
	return tool.NewFunc(tool.FuncConfig{Name: readReportToolName, Description: "Read an immutable report by index."},
		func(_ context.Context, input readReport) (string, error) {
			if input.Index < 0 || input.Index >= len(task.Documents) {
				return "", errors.New("report index is out of range")
			}
			encoded, encodeErr := jsonv2.Marshal(task.Documents[input.Index])
			return string(encoded), encodeErr
		})
}

type contextProgramConfig struct {
	Task   contextTask
	Model  chat.Model
	Policy contextPolicy
	Reader tool.Tool
}

func compileContext(compiler *agentrun.Compiler, config contextProgramConfig) (agentrun.Program[interaction.Input, interaction.Output], error) {
	task, policy := config.Task, config.Policy
	if policy.ID == "" {
		return agentrun.Program[interaction.Input, interaction.Output]{}, errors.New("context policy revision is required")
	}
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{Tools: []tool.Tool{config.Reader}, Revision: task.ID})
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	return interaction.Compile(compiler, interaction.Config{
		Name: "example.context_reduction", Description: "Read reports and preserve every latest fact and constraint.",
		Model: config.Model, ContextProcessor: policy.Reducer, Revision: task.ID + ":" + policy.ID,
		MaxModelCalls: agentrun.NewQuota(uint64(len(task.Documents) + 1)), Tools: tools,
	})
}

func runTask(ctx context.Context, task contextTask, policy contextPolicy) (recorded trajectory.Trajectory, runErr error) {
	reader, err := reportTool(task)
	if err != nil {
		return recorded, err
	}
	runtime, err := agentrun.New(ctx, agentrun.Config{Store: agentrun.NewMemoryStore(), Build: contextBuild})
	if err != nil {
		return recorded, err
	}
	defer func() { runErr = errors.Join(runErr, closeRuntime(ctx, runtime)) }()
	program, err := compileContext(runtime.Compiler(), contextProgramConfig{Task: task, Model: contextModel{rounds: len(task.Documents)}, Policy: policy, Reader: reader})
	if err != nil {
		return recorded, err
	}
	root, err := runtime.Start(ctx, program, agentrun.StartRequest[interaction.Input]{ID: agentrun.StartID{Scope: "example", Key: startrand.Text()}, Input: contextInput()})
	if err != nil {
		return recorded, err
	}
	if _, err := root.Await(ctx); err != nil {
		return recorded, err
	}
	return captureContext(ctx, root)
}

func captureContext(ctx context.Context, root *agentrun.Handle[interaction.Output]) (trajectory.Trajectory, error) {
	raw, err := trajectory.Capture(ctx, root, nil)
	if err != nil {
		return trajectory.Trajectory{}, err
	}
	log, err := raw.Log()
	if err != nil {
		return trajectory.Trajectory{}, err
	}
	coverage := new(trajectory.Coverage)
	for _, entry := range log.Entries {
		intent, ok := entry.Record.(agentrun.EffectIntent)
		if !ok || intent.Effect.Target() != agentrun.EffectTargetDispatcher {
			continue
		}
		_, model, err := interaction.ModelRequestFromEffect(intent.Effect)
		if err != nil {
			return trajectory.Trajectory{}, err
		}
		if model {
			coverage.Models = append(coverage.Models, intent.ID)
			continue
		}
		_, _, call, err := interaction.ToolCallFromEffect(intent.Effect)
		if err != nil {
			return trajectory.Trajectory{}, err
		}
		if call {
			coverage.Tools = append(coverage.Tools, intent.ID)
		} else {
			coverage.Other = append(coverage.Other, intent.ID)
		}
	}
	return trajectory.New(trajectory.Config{Log: log, Coverage: coverage})
}

func contextInput() interaction.Input {
	return interaction.Input{Messages: []chat.Message{chat.NewSystemMessage(contextRule), chat.NewUserMessage(chat.NewTextPart(contextGoal))}}
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

func closeRuntime(ctx context.Context, runtime *agentrun.Runtime) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), treeCleanupTimeout)
	defer cancel()
	return runtime.Close(cleanup)
}
