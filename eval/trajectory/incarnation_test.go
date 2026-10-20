package trajectory_test

import (
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestRecorderSeparatesOverlappingRootIncarnations(t *testing.T) {
	for _, discard := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			schema, err := jsonschema.For[string]()
			if err != nil {
				t.Fatal(err)
			}
			definition, err := coordination.NewInputGate(coordination.InputGateConfig{Name: "test.recording", Description: "Wait for recorded input.", RequestSchema: schema, AnswerSchema: schema})
			if err != nil {
				t.Fatal(err)
			}
			deployment, err := agent.NewDeployment(agent.DeploymentConfig{Definition: definition, ImplementationDigest: agent.ComputeDigest([]byte("recording")), ConfigurationDigest: agent.ComputeDigest([]byte("recording"))})
			if err != nil {
				t.Fatal(err)
			}
			recorder := new(trajectory.Recorder)
			config := agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), EventListeners: []agent.EventListener{recorder}}
			firstEngine, err := agent.NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			input, err := agent.EncodePayload("question")
			if err != nil {
				t.Fatal(err)
			}
			first, err := firstEngine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			cut, err := firstEngine.CaptureTree(t.Context(), first.Relation().ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			secondEngine, err := agent.NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			second, err := secondEngine.RestoreTree(t.Context(), deployment, cut)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if first.TreeIncarnationID() == second.TreeIncarnationID() {
				t.Fatal("restore reused an activation")
			}
			if killErr := first.Kill(t.Context(), "retire old writer"); killErr != nil {
				t.Fatal(killErr)
			}
			if joinErr := first.Join(t.Context()); !errors.Is(joinErr, agent.ErrTreeIncarnationConflict) {
				t.Fatalf("old writer=%v", joinErr)
			}
			if discard {
				recorder.Discard(first)
			} else {
				recorded, takeErr := recorder.Take(t.Context(), first, nil)
				if takeErr != nil {
					t.Fatal(takeErr)
				}
				for _, event := range recorded.Events() {
					id, _ := event.TreeIncarnationID()
					if id != first.TreeIncarnationID() {
						t.Fatal("old activation consumed new events")
					}
				}
			}
			if _, takeErr := recorder.Take(t.Context(), first, nil); !errors.Is(takeErr, trajectory.ErrIncompleteRecording) {
				t.Fatalf("consumed activation=%v", takeErr)
			}
			if killErr := second.Kill(t.Context(), "finish new writer"); killErr != nil {
				t.Fatal(killErr)
			}
			if joinErr := second.Join(t.Context()); joinErr != nil {
				t.Fatal(joinErr)
			}
			terminalCut, err := secondEngine.CaptureTree(t.Context(), second.Relation().ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			terminalEngine, err := agent.NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := terminalEngine.RestoreTree(t.Context(), deployment, terminalCut)
			if err != nil {
				t.Fatal(err)
			}
			if _, takeErr := recorder.Take(t.Context(), terminal, nil); !errors.Is(takeErr, trajectory.ErrIncompleteRecording) {
				t.Fatalf("terminal restoration borrowed previous observations: %v", takeErr)
			}
			if closeErr := terminalEngine.Close(t.Context()); closeErr != nil {
				t.Fatal(closeErr)
			}
			recorded, err := recorder.Take(t.Context(), second, nil)
			if err != nil {
				t.Fatal(err)
			}
			if recorded.HistoryComplete() {
				t.Fatal("restored activation claimed complete history")
			}
			for _, event := range recorded.Events() {
				id, _ := event.TreeIncarnationID()
				if id != second.TreeIncarnationID() {
					t.Fatal("new activation consumed old events")
				}
			}
			if err := firstEngine.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := secondEngine.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
