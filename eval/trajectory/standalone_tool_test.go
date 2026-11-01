package trajectory_test

import (
	startrand "crypto/rand"

	"context"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

// A ToolSet run as a root has no requesting Interaction; its call is still a
// complete, valid record.
func TestStandaloneToolSetRecordsAValidCall(t *testing.T) {
	recorder := &trajectory.Recorder{}
	tools, err := agent.NewToolSet(agent.ToolSetConfig{
		Name: "test.trajectory.standalone", Description: "Run one Tool call directly.", Tools: []tool.Tool{fixtureWeatherTool{}}, Observer: recorder,
		ImplementationDigest: run.ComputeDigest([]byte("standalone-implementation")), ConfigurationDigest: run.ComputeDigest([]byte("standalone-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), EventListeners: []run.EventListener{recorder}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := run.EncodePayload(map[string]chat.ToolCall{"call": {ID: "call", Name: "weather", Arguments: `{"city":"Paris"}`}})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), tools.Program(), run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
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
	if len(calls) != 1 || calls[0].ProcessID != process.Relation().ProcessID() || calls[0].Outcome() != trajectory.ToolOutcomeSucceeded {
		t.Fatalf("standalone Tool calls = %+v", calls)
	}
	if _, err := trajectory.New(trajectoryConfig(recorded)); err != nil {
		t.Fatalf("standalone Tool trajectory rejected: %v", err)
	}
}
