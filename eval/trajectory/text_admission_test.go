package trajectory_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/eval/trajectory"
)

func TestRecordedCallsRejectUnencodableFailureDiagnostics(t *testing.T) {
	recorder := new(trajectory.Recorder)
	process := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{})
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	model := recorded.ModelCalls()[0]
	model.Response, model.Unknown, model.Failure = nil, true, "\xff"
	if validationErr := model.Validate(); !errors.Is(validationErr, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("model Validate() = %v", validationErr)
	}
	call := recorded.ToolCalls()[0]
	call.Result, call.Unknown, call.Failure = nil, true, "\xff"
	if validationErr := call.Validate(); !errors.Is(validationErr, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("tool Validate() = %v", validationErr)
	}
	model.Failure, call.Failure = "失败\x00é", "失败\x00é"
	if validationErr := model.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	if validationErr := call.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	encoded, err := jsonv2.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	var decoded trajectory.ToolCall
	if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil || decoded.Failure != call.Failure {
		t.Fatalf("failure round trip = %q, %v", decoded.Failure, decodeErr)
	}
}

func TestToolExpectationRejectsUnencodableName(t *testing.T) {
	if validationErr := (trajectory.ToolExpectation{Name: "\xff"}).Validate(); !errors.Is(validationErr, trajectory.ErrInvalidSample) {
		t.Fatalf("Validate() = %v", validationErr)
	}
	if validationErr := (trajectory.ToolExpectation{Name: "工具\x00é"}).Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
}
