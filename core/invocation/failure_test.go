package invocation_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/invocation"
)

func TestFailureScopesExecutionEvidenceWithoutHidingCauses(t *testing.T) {
	cause := errors.New("request blocked")
	inner := &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: cause}
	failed := &invocation.Failure{Kind: invocation.FailureFailed, Cause: fmt.Errorf("continuation: %w", inner)}
	unknown := &invocation.Failure{Kind: invocation.FailureUnknown, Cause: inner}
	for _, test := range []struct {
		name string
		err  error
		want invocation.FailureKind
	}{
		{name: "absent", want: invocation.FailureInvalid},
		{name: "ordinary", err: cause, want: invocation.FailureUnknown},
		{name: "not executed", err: inner, want: invocation.FailureNotExecuted},
		{name: "transparent wrapping", err: fmt.Errorf("adapter: %w", inner), want: invocation.FailureNotExecuted},
		{name: "single joined boundary", err: errors.Join(inner), want: invocation.FailureNotExecuted},
		{name: "completed earlier work", err: failed, want: invocation.FailureFailed},
		{name: "unknown current work", err: unknown, want: invocation.FailureUnknown},
		{name: "unowned joined cause", err: errors.Join(inner, cause), want: invocation.FailureUnknown},
		{name: "unowned joined empty wrapper", err: errors.Join(inner, emptyFailureJoin{}), want: invocation.FailureUnknown},
		{name: "competing boundaries", err: errors.Join(inner, failed), want: invocation.FailureUnknown},
		{name: "independent matching boundaries", err: errors.Join(inner, inner), want: invocation.FailureUnknown},
		{name: "owned joined diagnostics", err: &invocation.Failure{Kind: invocation.FailureFailed, Cause: errors.Join(inner, cause)}, want: invocation.FailureFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := invocation.ClassifyFailure(test.err); got != test.want {
				t.Fatalf("classification=%s want=%s error=%v", got, test.want, test.err)
			}
			if test.err != nil && !errors.Is(test.err, cause) {
				t.Fatal("execution scope hid the diagnostic cause")
			}
		})
	}
	if boundary, ok := errors.AsType[*invocation.Failure](failed); !ok || boundary != failed || !errors.Is(failed, inner) {
		t.Fatal("diagnostic inspection lost the composed failure chain")
	}
}

type emptyFailureJoin struct{}

func (emptyFailureJoin) Error() string   { return "unowned diagnostic" }
func (emptyFailureJoin) Unwrap() []error { return nil }

type nilFailureCause struct{}

func (*nilFailureCause) Error() string { panic("typed nil cause must not be called") }

func TestInvalidFailureNeverEstablishesExecutionEvidence(t *testing.T) {
	var nilBoundary *invocation.Failure
	var nilCause *nilFailureCause
	for _, failure := range []*invocation.Failure{
		nilBoundary,
		{},
		{Kind: "unsupported", Cause: errors.New("cause")},
		{Kind: invocation.FailureNotExecuted},
		{Kind: invocation.FailureFailed, Cause: nilCause},
	} {
		if !errors.Is(failure.Validate(), invocation.ErrInvalidFailure) || invocation.ClassifyFailure(failure) != invocation.FailureUnknown {
			t.Fatalf("invalid boundary established a result: %#v", failure)
		}
		if failure.Error() != invocation.ErrInvalidFailure.Error() {
			t.Fatal("invalid boundary diagnostic changed")
		}
	}
	if got := invocation.ClassifyFailure(fmt.Errorf("wrapper: %w", nilCause)); got != invocation.FailureUnknown {
		t.Fatalf("wrapped typed nil=%s", got)
	}
	if nilBoundary.Unwrap() != nil || (&invocation.Failure{Kind: invocation.FailureFailed, Cause: nilCause}).Unwrap() != nil {
		t.Fatal("invalid nil cause escaped the failure boundary")
	}
}
