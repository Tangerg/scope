package trajectory_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/eval/trajectory"
)

// The parent dispatches models and its children dispatch tools. Persistence
// checkpoints are Framework progress and add no external operation to coverage.
func interactionCoverage(events []agent.Event) *trajectory.Coverage {
	coverage := &trajectory.Coverage{}
	for _, event := range events {
		fact, started := event.EffectStarted()
		if !started || fact.Target() != agent.EffectTargetDispatcher {
			continue
		}
		id, _ := event.EffectID()
		incarnation, _ := event.TreeIncarnationID()
		reference := trajectory.EffectReference{ProcessID: event.ProcessID(), TreeIncarnationID: incarnation, EffectID: id}
		if event.Relation().IsRoot() {
			coverage.Models = append(coverage.Models, reference)
		} else {
			coverage.Tools = append(coverage.Tools, reference)
		}
	}
	return coverage
}

func TestCoverageClassifiesEffectsWithinOneDeployment(t *testing.T) {
	recorded := coveredInteraction(t)
	coverage := recorded.Coverage()
	if len(coverage.Models) != 2 || len(coverage.Tools) != 1 || len(coverage.Other) != 0 {
		t.Fatalf("coverage = %+v", coverage)
	}
	encoded, err := jsonv2.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	var restored trajectory.Trajectory
	if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(coverage, restored.Coverage()) {
		t.Fatal("coverage changed across the strict trajectory round trip")
	}
	for _, sample := range []struct {
		name   string
		change func(*trajectory.Coverage)
		want   error
	}{
		{"invalid identity", func(c *trajectory.Coverage) { c.Models[0].ProcessID = agent.ProcessID{} }, trajectory.ErrInvalidTrajectory},
		{"duplicate classification", func(c *trajectory.Coverage) { c.Other = append(c.Other, c.Models[0]) }, trajectory.ErrInvalidTrajectory},
		{"missing classification", func(c *trajectory.Coverage) { c.Tools = nil }, trajectory.ErrIncompleteRecording},
		{"tool declared as model", func(c *trajectory.Coverage) { c.Models = append(c.Models, c.Tools...); c.Tools = nil }, trajectory.ErrIncompleteRecording},
		{"extra classification", func(c *trajectory.Coverage) {
			unexpected := c.Tools[0]
			unexpected.EffectID, _ = agent.ParseEffectID("effect:unobserved")
			c.Other = append(c.Other, unexpected)
		}, trajectory.ErrIncompleteRecording},
		{"unobserved effect", func(c *trajectory.Coverage) { c.Models[0].EffectID, _ = agent.ParseEffectID("effect:unobserved") }, trajectory.ErrIncompleteRecording},
	} {
		t.Run(sample.name, func(t *testing.T) {
			config := trajectoryConfig(recorded)
			sample.change(config.Coverage)
			if _, err := trajectory.New(config); !errors.Is(err, sample.want) {
				t.Fatalf("classification error = %v, want %v", err, sample.want)
			}
		})
	}
}
