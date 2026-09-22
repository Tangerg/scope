package trajectory_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestRecordedNullOutputSurvivesJSON(t *testing.T) {
	recorded := runTrajectoryInput(t, fixtureInput{NullOutput: true})
	encoded, err := jsonv2.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"output":null`)) {
		t.Fatalf("null output is absent: %s", encoded)
	}
	var decoded trajectory.Trajectory
	if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if decoded.Output().IsZero() || string(decoded.Output().JSON()) != "null" {
		t.Fatal("null output was lost")
	}
	before, err := recorded.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	after, err := decoded.BehaviorDigest(rawOutputProjection)
	if err != nil || before != after {
		t.Fatalf("behavior changed: %s != %s: %v", before, after, err)
	}
	config := trajectoryConfig(decoded)
	config.Output = agent.Payload{}
	if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("completed trajectory accepted missing output: %v", err)
	}
}

func TestExpectedNullOutputRemainsAnAssertion(t *testing.T) {
	nullActual := runTrajectoryInput(t, fixtureInput{NullOutput: true})
	valueActual := runTrajectory(t)
	for _, test := range []struct {
		name         string
		wire         string
		present      bool
		valueVerdict eval.Verdict
	}{
		{"null", `{"status":"completed","output":null}`, true, eval.VerdictFail},
		{"absent", `{"status":"completed"}`, false, eval.VerdictPass},
	} {
		t.Run(test.name, func(t *testing.T) {
			var expected trajectory.Expectation
			if err := jsonv2.Unmarshal([]byte(test.wire), &expected); err != nil {
				t.Fatal(err)
			}
			encoded, err := jsonv2.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			if err := jsonv2.Unmarshal(encoded, &expected); err != nil {
				t.Fatal(err)
			}
			if expected.Output.Valid() != test.present || bytes.Contains(encoded, []byte(`"output"`)) != test.present {
				t.Fatalf("output presence changed: %s", encoded)
			}
			for _, actual := range []struct {
				record  trajectory.Trajectory
				verdict eval.Verdict
			}{{nullActual, eval.VerdictPass}, {valueActual, test.valueVerdict}} {
				report, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: actual.record, Expected: expected})
				if err != nil || report.Verdict != actual.verdict {
					t.Fatalf("verdict=%s want=%s error=%v", report.Verdict, actual.verdict, err)
				}
			}
		})
	}
}
