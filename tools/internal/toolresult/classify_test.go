package toolresult_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

func TestClassifyPreservesAmbiguousFailureAndResponse(t *testing.T) {
	unknown := errors.New("second operation outcome unknown")
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: errors.New("first operation failed"), Output: tool.NewTextOutput("first failed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := struct {
		Completed int `json:"completed"`
	}{Completed: 2}
	got := toolresult.Classify("batch", response, errors.Join(failure, unknown))
	if _, definite := tool.ClassifyFailure(got); definite || !errors.Is(got, unknown) || !errors.Is(got, failure) {
		t.Fatalf("joined outcomes became definite: %v", got)
	}
	evidence, ok := errors.AsType[*tool.CallError](got)
	if !ok || string(evidence.Evidence().Details) != `{"completed":2}` {
		t.Fatalf("acknowledged progress was lost: %v", got)
	}
}

func TestClassifyPreservesExistingOutcomeAndObservations(t *testing.T) {
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: tool.NewTextOutput("denied")})
	if err != nil {
		t.Fatal(err)
	}
	got := toolresult.Classify("call", struct{}{}, failure)
	if classified, definite := tool.ClassifyFailure(got); !definite || classified != failure {
		t.Fatalf("outcome owner replaced: %v", got)
	}
	unknown := errors.New("unknown")
	observation, err := tool.NewCallError(tool.CallErrorConfig{Cause: unknown, Evidence: tool.NewTextOutput("partial")})
	if err != nil {
		t.Fatal(err)
	}
	got = toolresult.Classify("call", struct{}{}, observation)
	if retained, ok := errors.AsType[*tool.CallError](got); !ok || retained != observation {
		t.Fatalf("observation owner replaced: %v", got)
	}
	got = toolresult.Classify("call", make(chan int), unknown)
	if !errors.Is(got, unknown) {
		t.Fatalf("encoding failure lost cause: %v", got)
	}
}

func TestClassifySettlesOnlyAnUnclassifiedCorrectableError(t *testing.T) {
	invalid := errors.New("invalid input")
	got := toolresult.Classify("call", struct{}{}, fmt.Errorf("parse: %w", invalid), invalid)
	failure, definite := tool.ClassifyFailure(got)
	if !definite || failure.Kind() != tool.FailureKindFailed || !errors.Is(failure.Cause(), invalid) || !hasText(failure.Output(), "call: parse: invalid input") {
		t.Fatalf("correctable error = %v", got)
	}
	rejected, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: tool.NewTextOutput("denied")})
	if err != nil {
		t.Fatal(err)
	}
	got = toolresult.Classify("call", struct{}{}, errors.Join(rejected, invalid), invalid)
	if _, definite := tool.ClassifyFailure(got); definite {
		t.Fatalf("a correctable error joined with an outcome became definite: %v", got)
	}
	if _, ok := errors.AsType[*tool.CallError](got); !ok {
		t.Fatalf("joined outcomes lost their evidence: %v", got)
	}
}

func hasText(output tool.Output, want string) bool {
	text, ok := output.Text()
	return ok && text == want
}
