package toolresult_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

func TestWithEvidencePreservesAmbiguousFailureAndResponse(t *testing.T) {
	unknown := errors.New("second operation outcome unknown")
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: errors.New("first operation failed"), Output: chat.NewTextToolOutput("first failed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := struct {
		Completed int `json:"completed"`
	}{Completed: 2}
	got := toolresult.WithEvidence("batch", response, errors.Join(failure, unknown))
	if _, definite := tool.ClassifyFailure(got); definite || !errors.Is(got, unknown) || !errors.Is(got, failure) {
		t.Fatalf("joined outcomes became definite: %v", got)
	}
	evidence, ok := errors.AsType[*tool.CallError](got)
	if !ok || string(evidence.Evidence().Details) != `{"completed":2}` {
		t.Fatalf("acknowledged progress was lost: %v", got)
	}
}

func TestWithEvidencePreservesExistingOutcomeAndObservations(t *testing.T) {
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: chat.NewTextToolOutput("denied")})
	if err != nil {
		t.Fatal(err)
	}
	got := toolresult.WithEvidence("call", struct{}{}, failure)
	if classified, definite := tool.ClassifyFailure(got); !definite || classified != failure {
		t.Fatalf("outcome owner replaced: %v", got)
	}
	unknown := errors.New("unknown")
	observation, err := tool.NewCallError(tool.CallErrorConfig{Cause: unknown, Evidence: chat.NewTextToolOutput("partial")})
	if err != nil {
		t.Fatal(err)
	}
	got = toolresult.WithEvidence("call", struct{}{}, observation)
	if retained, ok := errors.AsType[*tool.CallError](got); !ok || retained != observation {
		t.Fatalf("observation owner replaced: %v", got)
	}
	got = toolresult.WithEvidence("call", make(chan int), unknown)
	if !errors.Is(got, unknown) {
		t.Fatalf("encoding failure lost cause: %v", got)
	}
}
