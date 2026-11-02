package trajectory_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestCoverageCannotClassifyOneEffectTwiceOrInventOwnership(t *testing.T) {
	actual := coveredInteraction(t)
	config := trajectoryConfig(t, actual)
	for _, coverage := range []*trajectory.Coverage{
		{Models: []run.EffectID{config.Coverage.Models[0], config.Coverage.Models[0]}},
		{Models: []run.EffectID{config.Coverage.Models[0]}, Other: []run.EffectID{config.Coverage.Models[0]}},
		{Tools: []run.EffectID{{}}},
		{Models: []run.EffectID{{Process: config.Log.Root, Step: 999, Index: 0}}},
		{Models: config.Coverage.Tools},
		{Tools: config.Coverage.Models},
	} {
		config.Coverage = coverage
		if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
			t.Fatalf("invalid classification admitted: %+v %v", coverage, err)
		}
	}
}

func TestMissingCoverageCannotProveNoCallsOrTokens(t *testing.T) {
	actual := coveredInteraction(t)
	for _, coverage := range []*trajectory.Coverage{nil, {}} {
		config := trajectoryConfig(t, actual)
		config.Coverage = coverage
		incomplete, err := trajectory.New(config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := incomplete.TotalTokens(); !errors.Is(err, trajectory.ErrIncompleteRecording) {
			t.Fatal("missing coverage proved zero tokens", err)
		}
		if _, err := incomplete.BehaviorDigest(semanticInteractionOutput); !errors.Is(err, trajectory.ErrIncompleteRecording) {
			t.Fatal("missing coverage proved behavior", err)
		}
		if len(incomplete.ModelCalls()) != 0 || len(incomplete.ToolCalls()) != 0 {
			t.Fatal("opaque payloads were guessed")
		}
	}
}

func TestMalformedOrForeignLogCannotBecomeEvaluationEvidence(t *testing.T) {
	actual := coveredInteraction(t)
	for _, test := range []struct {
		name   string
		change func(*run.Log)
	}{
		{"missing position", func(log *run.Log) { log.Entries = append(log.Entries[:2], log.Entries[3:]...) }},
		{"duplicate position", func(log *run.Log) { log.Entries[2].LSN = log.Entries[1].LSN }},
		{"foreign effect", func(log *run.Log) {
			for i, entry := range log.Entries {
				if intent, ok := entry.Record.(run.EffectIntent); ok {
					intent.ID.Process = run.StartID{Scope: "foreign", Key: "root"}.Root()
					log.Entries[i].Record = intent
					return
				}
			}
		}},
		{"duplicate terminal", func(log *run.Log) {
			last := log.Entries[len(log.Entries)-1]
			last.LSN++
			log.Entries = append(log.Entries, last)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := trajectoryConfig(t, actual)
			test.change(&config.Log)
			if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
				t.Fatalf("corrupted log admitted: %v", err)
			}
		})
	}
}

func TestTrajectoryWireAdmitsOnlyLogAndCoverage(t *testing.T) {
	actual := coveredInteraction(t)
	encoded, err := jsonv2.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"events", "model_calls", "tool_calls", "termination", "root_usage", "elapsed_ns", "gaps", "version"} {
		corrupted := append([]byte(`{"`+field+`":{},`), bytes.TrimPrefix(encoded, []byte("{"))...)
		var restored trajectory.Trajectory
		if err := jsonv2.Unmarshal(corrupted, &restored); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
			t.Fatalf("independent %s admitted: %v", field, err)
		}
	}
}
