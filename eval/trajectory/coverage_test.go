package trajectory_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/eval/trajectory"
)

// This fixture runs a model/commit parent and ordinary Tool children. Commit
// receipts identify the parent's non-model Effects independently of responses.
type fixtureResultCommits struct {
	mu      sync.Mutex
	effects map[trajectory.EffectReference]struct{}
}

func (f *fixtureResultCommits) CommitResults(_ context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
	incarnation, _ := batch.TreeIncarnationID()
	receipt := batch.Receipt()
	f.mu.Lock()
	f.effects[trajectory.EffectReference{ProcessID: batch.Relation().ProcessID(), TreeIncarnationID: incarnation, EffectID: receipt.EffectID}] = struct{}{}
	f.mu.Unlock()
	return receipt, nil
}

func (f *fixtureResultCommits) coverage(events []agent.Event) *trajectory.Coverage {
	f.mu.Lock()
	defer f.mu.Unlock()
	coverage := &trajectory.Coverage{}
	for _, event := range events {
		fact, started := event.EffectStarted()
		if !started || fact.Target() != agent.EffectTargetDispatcher {
			continue
		}
		id, _ := event.EffectID()
		incarnation, _ := event.TreeIncarnationID()
		reference := trajectory.EffectReference{ProcessID: event.ProcessID(), TreeIncarnationID: incarnation, EffectID: id}
		if _, committed := f.effects[reference]; committed {
			coverage.Other = append(coverage.Other, reference)
		} else if event.Relation().IsRoot() {
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
	if len(coverage.Models) != 2 || len(coverage.Tools) != 1 || len(coverage.Other) != 1 {
		t.Fatalf("coverage = %+v", coverage)
	}
	if coverage.Models[0].ProcessID != coverage.Other[0].ProcessID {
		t.Fatal("fixture did not mix model and commit Effects in one Process")
	}
	encoded, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	var restored trajectory.Trajectory
	if err := json.Unmarshal(encoded, &restored); err != nil {
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
		{"missing classification", func(c *trajectory.Coverage) { c.Other = nil }, trajectory.ErrIncompleteRecording},
		{"commit declared as model", func(c *trajectory.Coverage) { c.Models = append(c.Models, c.Other...); c.Other = nil }, trajectory.ErrIncompleteRecording},
		{"extra classification", func(c *trajectory.Coverage) {
			unexpected := c.Other[0]
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
