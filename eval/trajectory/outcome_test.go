package trajectory_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestTrajectoryRequiresAgreementWithRootFinishedEvent(t *testing.T) {
	recorder := &trajectory.Recorder{}
	result := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{
		failure: interaction.HostFailure(errors.New("fixture host failure")),
	})
	recorded, err := recorder.Take(result)
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
		cause               agent.TerminationCause
		conflict            bool
	}
	cases := []outcomeCase{
		{name: "unchanged", kind: original.Kind(), cause: recorded.Termination().Cause(), code: original.Code(), message: original.Message()},
		{name: "diagnostic only", kind: original.Kind(), cause: recorded.Termination().Cause(), code: original.Code(), message: "another diagnostic"},
		{name: "failure code", kind: original.Kind(), cause: recorded.Termination().Cause(), code: "test.different", message: original.Message(), conflict: true},
	}
	for _, classification := range []struct {
		kind  agent.FailureKind
		cause agent.TerminationCause
	}{
		{kind: agent.FailureKindExecution, cause: agent.TerminationCauseExecutionFailure},
		{kind: agent.FailureKindContract, cause: agent.TerminationCauseContractFailure},
		{kind: agent.FailureKindExternal, cause: agent.TerminationCauseExternalFailure},
		{kind: agent.FailureKindPanic, cause: agent.TerminationCausePanic},
	} {
		if classification.kind != original.Kind() {
			cases = append(cases, outcomeCase{
				name: classification.kind.String(), kind: classification.kind, cause: classification.cause,
				code: original.Code(), message: original.Message(), conflict: true,
			})
		}
	}
	encoded, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failure, err := agent.NewFailure(testCase.kind, testCase.code, testCase.message)
			if err != nil {
				t.Fatal(err)
			}
			termination := trajectoryFailureTermination(t, recorded.Termination(), testCase.cause, failure)
			constructed, constructErr := trajectory.New(trajectory.Config{
				RootProcessID: recorded.RootProcessID(), Termination: termination,
				Usage: recorded.Usage(), Duration: recorded.Duration(), Events: recorded.Events(),
				ModelCalls: recorded.ModelCalls(), ToolCalls: recorded.ToolCalls(),
			})
			var wire map[string]json.RawMessage
			if wireErr := json.Unmarshal(encoded, &wire); wireErr != nil {
				t.Fatal(wireErr)
			}
			wire["termination"], err = json.Marshal(termination)
			if err != nil {
				t.Fatal(err)
			}
			modified, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var decoded trajectory.Trajectory
			decodeErr := json.Unmarshal(modified, &decoded)
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

func trajectoryFailureTermination(t *testing.T, original agent.Termination, cause agent.TerminationCause, failure agent.Failure) agent.Termination {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Status              agent.Status           `json:"status"`
		Cause               agent.TerminationCause `json:"cause"`
		Reason              string                 `json:"reason"`
		Failure             agent.Failure          `json:"failure"`
		UnresolvedEffectIDs []agent.EffectID       `json:"unresolved_effect_ids,omitempty"`
	}{
		Status: original.Status(), Cause: cause, Reason: failure.Message(), Failure: failure,
		UnresolvedEffectIDs: original.UnresolvedEffectIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var termination agent.Termination
	if err := json.Unmarshal(encoded, &termination); err != nil {
		t.Fatal(err)
	}
	return termination
}
