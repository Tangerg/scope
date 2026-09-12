package trajectory_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/eval/trajectory"
)

func trajectoryConfig(recorded trajectory.Trajectory) trajectory.Config {
	return trajectory.Config{RootProcessID: recorded.RootProcessID(), Termination: recorded.Termination(), Output: recorded.Output(), RootUsage: recorded.RootUsage(), Elapsed: recorded.Elapsed(), Coverage: recorded.Coverage(), Events: recorded.Events(), ModelCalls: recorded.ModelCalls(), ToolCalls: recorded.ToolCalls()}
}

func coveredInteraction(t *testing.T) trajectory.Trajectory {
	t.Helper()
	recorder := &trajectory.Recorder{}
	process := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{})
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := trajectoryConfig(recorded)
	config.Coverage = &trajectory.Coverage{}
	for _, event := range config.Events {
		if event.Name() != agent.EventProcessStarted {
			continue
		}
		if event.Relation().IsRoot() {
			config.Coverage.Models = append(config.Coverage.Models, event.DeploymentRef())
		} else {
			config.Coverage.Tools = append(config.Coverage.Tools, event.DeploymentRef())
		}
	}
	for i := range config.ModelCalls {
		config.ModelCalls[i].Response.Metadata = &chat.ResponseMetadata{Usage: &chat.Usage{InputTokens: 3, OutputTokens: 2}}
	}
	recorded, err = trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

func TestMissingEvidenceCannotProveZeroCost(t *testing.T) {
	recorded := coveredInteraction(t)
	config := trajectoryConfig(recorded)
	config.ModelCalls = nil
	if _, checkErr := trajectory.New(config); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("missing model calls = %v", checkErr)
	}
	config = trajectoryConfig(recorded)
	for i := range config.ModelCalls {
		config.ModelCalls[i].Response.Metadata = nil
	}
	missing, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr := missing.TotalTokens(); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("missing accounting = %v", checkErr)
	}
	config = trajectoryConfig(recorded)
	for i := range config.ModelCalls {
		config.ModelCalls[i].Response.Metadata.Usage = nil
	}
	missing, err = trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr := missing.TotalTokens(); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("metadata without accounting = %v", checkErr)
	}
	config = trajectoryConfig(recorded)
	config.ToolCalls = nil
	if _, checkErr := trajectory.New(config); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("missing tools = %v", checkErr)
	}
	config = trajectoryConfig(recorded)
	config.Coverage = nil
	unknown, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr := unknown.TotalTokens(); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("undeclared coverage = %v", checkErr)
	}
	if _, checkErr := unknown.BehaviorDigest(rawOutputProjection); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("undeclared replay evidence = %v", checkErr)
	}
	noCalls := runTrajectory(t)
	if tokens, err := noCalls.TotalTokens(); err != nil || tokens != 0 {
		t.Fatalf("known model-free tree = %d, %v", tokens, err)
	}
}

func TestMissingChildTailIsNotACompleteTree(t *testing.T) {
	config := trajectoryConfig(coveredInteraction(t))
	for i, event := range config.Events {
		if !event.Relation().IsRoot() && event.Name() == agent.EventProcessFinished {
			config.Events = append(config.Events[:i:i], config.Events[i+1:]...)
			break
		}
	}
	if _, checkErr := trajectory.New(config); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("missing child tail = %v", checkErr)
	}
}

func TestSemanticProjectionCoversRealInteractionOutput(t *testing.T) {
	base := coveredInteraction(t)
	project := func(output agent.Output) (json.RawMessage, error) {
		value, err := output.Decode[interaction.Output]()
		if err != nil {
			return nil, err
		}
		return json.Marshal(value.ModelResponse.Text())
	}
	config := trajectoryConfig(base)
	value, err := config.Output.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	value.ModelResponse.Metadata = &chat.ResponseMetadata{ID: "another-response", CreatedAt: time.Now(), Usage: &chat.Usage{InputTokens: 800, OutputTokens: 300}}
	output, err := agent.EncodeOutput(value)
	if err != nil {
		t.Fatal(err)
	}
	config.Output = &output
	candidate, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	baselineDigest, err := base.BehaviorDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := candidate.BehaviorDigest(project)
	if err != nil || digest != baselineDigest {
		t.Fatalf("accounting changed semantic digest: %s, %v", digest, err)
	}
	value.ModelResponse.Output.Message.Parts[0].Text = "rainy"
	output, err = agent.EncodeOutput(value)
	if err != nil {
		t.Fatal(err)
	}
	config.Output = &output
	changed, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	digest, err = changed.BehaviorDigest(project)
	if err != nil || digest == baselineDigest {
		t.Fatalf("business output did not change digest: %s, %v", digest, err)
	}
	if _, checkErr := base.BehaviorDigest(nil); !errors.Is(checkErr, trajectory.ErrInvalidSample) {
		t.Fatalf("implicit projection = %v", checkErr)
	}
}

func changeEvent(t *testing.T, event agent.Event, fields map[string]any) agent.Event {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if checkErr := json.Unmarshal(data, &wire); checkErr != nil {
		t.Fatal(checkErr)
	}
	for key, value := range fields {
		wire[key], err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var changed agent.Event
	if checkErr := json.Unmarshal(data, &changed); checkErr != nil {
		t.Fatal(checkErr)
	}
	return changed
}

func TestActivationSegmentsAndWallClockRegressionRemainUsable(t *testing.T) {
	base := runTrajectory(t)
	config := trajectoryConfig(base)
	first := changeEvent(t, config.Events[0], map[string]any{"tree_incarnation_id": "incarnation:00000000000000000000000000000001"})
	for i, event := range config.Events {
		fields := map[string]any{"tree_incarnation_id": "incarnation:00000000000000000000000000000002", "occurred_at": time.Unix(100-int64(i), 0).UTC()}
		if i == 0 {
			fields["name"] = agent.EventProcessRestored
		}
		config.Events[i] = changeEvent(t, event, fields)
	}
	config.Events = append(config.Events, first)
	config.Elapsed = nil
	restored, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if restored.HistoryComplete() {
		t.Fatal("restored fragment claims full history")
	}
	if _, checkErr := restored.TotalTokens(); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("fragment tokens = %v", checkErr)
	}
	zero := time.Duration(0)
	if _, checkErr := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: restored, Expected: trajectory.Expectation{Status: agent.StatusCompleted, Limits: trajectory.Limits{Elapsed: &zero}}}); !errors.Is(checkErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("unknown elapsed satisfied zero budget: %v", checkErr)
	}
	encoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	var decoded trajectory.Trajectory
	if checkErr := json.Unmarshal(encoded, &decoded); checkErr != nil {
		t.Fatal(checkErr)
	}
	if len(decoded.Events()) != len(config.Events) {
		t.Fatal("activation segment lost on round trip")
	}
}

func TestObservationLossDoesNotChangeSemanticBehavior(t *testing.T) {
	base := coveredInteraction(t)
	config := trajectoryConfig(base)
	var events []agent.Event
	inserted := false
	rootSequence := uint64(0)
	for _, event := range config.Events {
		if !event.Relation().IsRoot() {
			events = append(events, event)
			continue
		}
		rootSequence++
		fields := map[string]any{"process_sequence": rootSequence}
		if event.Name() == agent.EventProcessFinished {
			var payload struct {
				Status agent.Status           `json:"process_status"`
				Cause  agent.TerminationCause `json:"termination_cause"`
				Usage  agent.Usage            `json:"usage"`
			}
			if checkErr := json.Unmarshal(event.Payload(), &payload); checkErr != nil {
				t.Fatal(checkErr)
			}
			payload.Usage.DroppedDeltas = 9
			fields["payload"] = payload
		}
		events = append(events, changeEvent(t, event, fields))
		if !inserted && event.Name() == agent.EventEffectStarted {
			rootSequence++
			dropped := changeEvent(t, event, map[string]any{"process_sequence": rootSequence, "name": agent.EventDeltaDropped, "payload": map[string]any{"dropped_delta_count": 9}})
			events = append(events, dropped)
			inserted = true
		}
	}
	config.Events = events
	config.RootUsage.DroppedDeltas = 9
	changed, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	before, err := base.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	after, err := changed.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("observation loss changed semantic behavior")
	}
}

func TestSignalArrivalTimingDoesNotChangeSemanticBehavior(t *testing.T) {
	base := coveredInteraction(t)
	var signal agent.Event
	for _, event := range base.Events() {
		if event.Name() == agent.EventSignalAccepted {
			signal = event
		}
	}
	if !signal.Valid() {
		t.Fatal("fixture did not accept a child completion signal")
	}
	var digests []string
	for _, arrival := range []string{agent.EventStepStarted, agent.EventStepCommitted} {
		config := trajectoryConfig(base)
		var events []agent.Event
		var rootSequence uint64
		for _, event := range config.Events {
			if !event.Relation().IsRoot() {
				events = append(events, event)
				continue
			}
			if event.Name() == agent.EventSignalAccepted {
				continue
			}
			rootSequence++
			fields := map[string]any{"process_sequence": rootSequence}
			step, _ := event.StepSequence()
			if step == 4 && event.Name() == agent.EventStepCommitted {
				status := agent.StatusWaiting
				if arrival == agent.EventStepStarted {
					status = agent.StatusRunning
				}
				fields["payload"] = map[string]any{"process_status": status}
			}
			events = append(events, changeEvent(t, event, fields))
			if step == 4 && event.Name() == arrival {
				rootSequence++
				events = append(events, changeEvent(t, signal, map[string]any{"process_sequence": rootSequence}))
			}
		}
		config.Events = events
		candidate, err := trajectory.New(config)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := candidate.BehaviorDigest(rawOutputProjection)
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, digest)
	}
	if digests[0] != digests[1] {
		t.Fatal("signal arrival or transient scheduling status changed semantic behavior")
	}
}

func TestTakeSealsEvidenceAgainstConcurrentLateCallbacks(t *testing.T) {
	source := &trajectory.Recorder{}
	process := runRecordedInteraction(t, source, source, fixtureWeatherTool{})
	recorded, err := source.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &trajectory.Recorder{}
	events := recorded.Events()
	for _, event := range events {
		recorder.OnEvent(t.Context(), event)
	}
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		for range 500 {
			recorder.OnEvent(t.Context(), events[len(events)-1])
		}
		close(done)
	}()
	<-started
	_, err = recorder.Take(t.Context(), process, nil)
	if err != nil && !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("late duplicate events: %v", err)
	}
	<-done
	if _, takeErr := recorder.Take(t.Context(), process, nil); !errors.Is(takeErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("consumed session reopened: %v", takeErr)
	}
}
