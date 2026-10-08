package trajectory_test

import (
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

// A ToolSet run as a root has no requesting Interaction, so its recorded call
// carries no model call or position rather than an invented one.
func TestStandaloneToolSetRecordsNoRequestingModelCall(t *testing.T) {
	recorder := &trajectory.Recorder{}
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "test.trajectory.standalone", Description: "Run one Tool call directly.", Tools: []tool.Tool{fixtureWeatherTool{}}, Observer: recorder,
		ImplementationDigest: agent.ComputeDigest([]byte("standalone-implementation")), ConfigurationDigest: agent.ComputeDigest([]byte("standalone-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), EventListeners: []agent.EventListener{recorder}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := agent.EncodePayload(map[string]chat.ToolCall{"call": {ID: "call", Name: "weather", Arguments: `{"city":"Paris"}`}})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), tools.Deployment(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := process.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := recorded.ToolCalls()
	if len(calls) != 1 || calls[0].ModelCall != 0 || calls[0].Index != 0 || calls[0].Outcome() != trajectory.ToolOutcomeSucceeded {
		t.Fatalf("standalone Tool calls = %+v", calls)
	}
	if _, err := trajectory.New(trajectoryConfig(recorded)); err != nil {
		t.Fatalf("standalone Tool trajectory rejected: %v", err)
	}
}
