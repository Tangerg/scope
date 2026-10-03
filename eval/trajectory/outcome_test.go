package trajectory_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestTrajectoryRequiresAgreementWithRootFinishedEvent(t *testing.T) {
	recorder := &trajectory.Recorder{}
	process, _ := startRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{}, 1)
	_, err := process.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	original, failed := recorded.Termination().Failure()
	if !failed {
		t.Fatal("fixture did not fail")
	}
	type outcomeCase struct {
		name, code, message string
		kind                agent.FailureKind
		conflict            bool
	}
	cases := []outcomeCase{
		{name: "unchanged", kind: original.Kind(), code: original.Code(), message: original.Message()},
		{name: "diagnostic only", kind: original.Kind(), code: original.Code(), message: "another diagnostic"},
		{name: "failure code", kind: original.Kind(), code: "test.different", message: original.Message(), conflict: true},
	}
	for _, kind := range []agent.FailureKind{
		agent.FailureKindExecution, agent.FailureKindContract, agent.FailureKindExternal, agent.FailureKindPanic,
	} {
		if kind != original.Kind() {
			cases = append(cases, outcomeCase{
				name: kind.String(), kind: kind,
				code: original.Code(), message: original.Message(), conflict: true,
			})
		}
	}
	encoded, err := jsonv2.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failure, err := agent.NewFailure(testCase.kind, testCase.code, testCase.message)
			if err != nil {
				t.Fatal(err)
			}
			termination := trajectoryFailureTermination(t, recorded.Termination(), failure)
			constructed, constructErr := trajectory.New(trajectory.Config{
				RootProcessID: recorded.RootProcessID(), Termination: termination,
				RootUsage: recorded.RootUsage(), Elapsed: recorded.Elapsed(), Events: recorded.Events(),
				ModelCalls: recorded.ModelCalls(), ToolCalls: recorded.ToolCalls(),
			})
			var wire map[string]json.RawMessage
			if wireErr := jsonv2.Unmarshal(encoded, &wire); wireErr != nil {
				t.Fatal(wireErr)
			}
			wire["termination"], err = jsonv2.Marshal(termination)
			if err != nil {
				t.Fatal(err)
			}
			modified, err := jsonv2.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var decoded trajectory.Trajectory
			decodeErr := jsonv2.Unmarshal(modified, &decoded)
			if testCase.conflict {
				if !errors.Is(constructErr, trajectory.ErrInvalidTrajectory) || !errors.Is(decodeErr, trajectory.ErrInvalidTrajectory) {
					t.Fatalf("conflicting root outcome: New error %v, UnmarshalJSON error %v", constructErr, decodeErr)
				}
				return
			}
			if constructErr != nil || decodeErr != nil {
				t.Fatalf("consistent root outcome: New error %v, UnmarshalJSON error %v", constructErr, decodeErr)
			}
			for _, actual := range []trajectory.Trajectory{constructed, decoded} {
				got, present := actual.Termination().Failure()
				if !present || got != failure {
					t.Fatalf("failure = %#v, present %v; want %#v", got, present, failure)
				}
			}
		})
	}
}

func trajectoryFailureTermination(t *testing.T, original agent.Termination, failure agent.Failure) agent.Termination {
	t.Helper()
	encoded, err := jsonv2.Marshal(struct {
		Failure             agent.Failure    `json:"failure"`
		UnresolvedEffectIDs []agent.EffectID `json:"unresolved_effect_ids,omitempty"`
	}{
		Failure:             failure,
		UnresolvedEffectIDs: original.UnresolvedEffectIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var termination agent.Termination
	if err := jsonv2.Unmarshal(encoded, &termination); err != nil {
		t.Fatal(err)
	}
	return termination
}
