package trajectory_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/eval/trajectory"
)

func TestToolExpectationRejectsUnencodableName(t *testing.T) {
	if validationErr := (trajectory.ToolExpectation{Name: "\xff"}).Validate(); !errors.Is(validationErr, trajectory.ErrInvalidSample) {
		t.Fatalf("Validate() = %v", validationErr)
	}
	if validationErr := (trajectory.ToolExpectation{Name: "工具\x00é"}).Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
}
