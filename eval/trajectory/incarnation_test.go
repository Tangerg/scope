package trajectory_test

import (
	startrand "crypto/rand"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/coordination"

	"github.com/Tangerg/scope/agent/run"
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
			deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{Definition: definition, ImplementationDigest: run.ComputeDigest([]byte("recording")), ConfigurationDigest: run.ComputeDigest([]byte("recording"))})
			if err != nil {
				t.Fatal(err)
			}
			recorder := new(trajectory.Recorder)
			config := run.Config{Store: run.NewMemoryStore(), EventListeners: []run.EventListener{recorder}}
			firstEngine, err := run.New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			input, err := run.EncodePayload("question")
			if err != nil {
				t.Fatal(err)
			}
			first, err := firstEngine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			cut, err := firstEngine.CaptureTree(t.Context(), first.Relation().ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			secondEngine, err := run.New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			second, err := secondEngine.Recover(t.Context(), deployment, cut)
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
			if joinErr := first.Join(t.Context()); !errors.Is(joinErr, run.ErrTreeIncarnationConflict) {
				t.Fatalf("old writer=%v", joinErr)
			}
			if discard {
				if discardErr := recorder.Discard(t.Context(), first); discardErr != nil {
					t.Fatal(discardErr)
				}
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
			terminalEngine, err := run.New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := terminalEngine.Recover(t.Context(), deployment, terminalCut)
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
