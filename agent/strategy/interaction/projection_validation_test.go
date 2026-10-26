package interaction

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestSettledResultsRejectsCompletionMarkersThatRetainActiveFacts(t *testing.T) {
	base := completedProjectionTree(t)
	for _, state := range []executionState{
		{Completed: true, ModelCallCount: 1},
		{Completed: true, WorkingContext: &Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("retained"))}}},
		{Completed: true, ToolRound: &toolCallRound{}},
	} {
		snapshot := projectionTreeWithState(t, base, state)
		if rounds, err := SettledResults(snapshot); len(rounds) != 0 || !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("invalid completion hid recovery facts: rounds=%v error=%v", rounds, err)
		}
	}
	if rounds, err := SettledResults(base); err != nil || len(rounds) != 0 {
		t.Fatalf("canonical completion changed: rounds=%v error=%v", rounds, err)
	}
}

func TestSettledResultsRejectsMalformedRetainedRounds(t *testing.T) {
	base := completedProjectionTree(t)
	for _, test := range []struct {
		name   string
		mutate func(*executionState)
	}{
		{"missing working context", func(s *executionState) { s.WorkingContext = nil }},
		{"invalid working context", func(s *executionState) { s.WorkingContext.Messages = nil }},
		{"missing response", func(s *executionState) { s.ToolRound.Response = nil }},
		{"no model invocation", func(s *executionState) { s.ModelCallCount = 0 }},
		{"no calls", func(s *executionState) {
			message := chat.NewAssistantMessage(chat.NewTextPart("done"))
			s.ToolRound.Response = &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}
		}},
		{"extra result", func(s *executionState) { s.ToolRound.Results = append(s.ToolRound.Results, s.ToolRound.Results[0]) }},
		{"invalid result", func(s *executionState) { s.ToolRound.Results[0].Disposition = ResultInvalid }},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}))
			state := executionState{
				WorkingContext: &Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("inspect"))}},
				ModelCallCount: 1,
				ToolRound: &toolCallRound{
					Response: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}},
					Results:  []toolCallResult{{Disposition: ResultSucceeded, Output: chat.NewTextToolOutput("done")}},
				},
			}
			test.mutate(&state)
			if rounds, err := SettledResults(projectionTreeWithState(t, base, state)); err == nil || len(rounds) != 0 {
				t.Fatalf("malformed round produced a publication: %+v, %v", rounds, err)
			}
		})
	}
}

func completedProjectionTree(t testing.TB) agent.TreeSnapshot {
	t.Helper()
	definition, err := NewDefinition(DefinitionConfig{Name: "interaction.projection", Description: "Validate retained projection evidence."})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewDispatcher(definition, DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		message := chat.NewAssistantMessage(chat.NewTextPart("done"))
		return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("projection")), ConfigurationDigest: agent.ComputeDigest([]byte("projection-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := agent.EncodePayload(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := engine.CaptureTree(t.Context(), result.ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func projectionTreeWithState(t testing.TB, snapshot agent.TreeSnapshot, state executionState) agent.TreeSnapshot {
	t.Helper()
	var tree map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &tree); err != nil {
		t.Fatal(err)
	}
	var processes []map[string]json.RawMessage
	if err := jsonv2.Unmarshal(tree["process_snapshots"], &processes); err != nil {
		t.Fatal(err)
	}
	envelope, err := agent.EncodeExecutionState(executionStateKind, state)
	if err != nil {
		t.Fatal(err)
	}
	processes[0]["committed_execution_state"], err = jsonv2.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	tree["process_snapshots"], err = jsonv2.Marshal(processes)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := agent.ParseTreeSnapshot(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}
