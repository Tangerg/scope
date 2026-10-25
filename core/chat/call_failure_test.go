package chat_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestCallFailureScopesExecutionEvidenceWithoutHidingCauses(t *testing.T) {
	cause := errors.New("request blocked")
	inner := &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: cause}
	failed := &chat.CallFailure{Kind: chat.CallFailureFailed, Cause: fmt.Errorf("continuation: %w", inner)}
	unknown := &chat.CallFailure{Kind: chat.CallFailureUnknown, Cause: inner}
	for _, test := range []struct {
		name string
		err  error
		want chat.CallFailureKind
	}{
		{name: "absent", want: chat.CallFailureInvalid},
		{name: "ordinary", err: cause, want: chat.CallFailureUnknown},
		{name: "not executed", err: inner, want: chat.CallFailureNotExecuted},
		{name: "transparent wrapping", err: fmt.Errorf("adapter: %w", inner), want: chat.CallFailureNotExecuted},
		{name: "single joined boundary", err: errors.Join(inner), want: chat.CallFailureNotExecuted},
		{name: "completed earlier work", err: failed, want: chat.CallFailureFailed},
		{name: "unknown current work", err: unknown, want: chat.CallFailureUnknown},
		{name: "unowned joined cause", err: errors.Join(inner, cause), want: chat.CallFailureUnknown},
		{name: "unowned joined empty wrapper", err: errors.Join(inner, emptyFailureJoin{}), want: chat.CallFailureUnknown},
		{name: "competing boundaries", err: errors.Join(inner, failed), want: chat.CallFailureUnknown},
		{name: "independent matching boundaries", err: errors.Join(inner, inner), want: chat.CallFailureUnknown},
		{name: "owned joined diagnostics", err: &chat.CallFailure{Kind: chat.CallFailureFailed, Cause: errors.Join(inner, cause)}, want: chat.CallFailureFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := chat.ClassifyFailure(test.err); got != test.want {
				t.Fatalf("classification=%s want=%s error=%v", got, test.want, test.err)
			}
			if test.err != nil && !errors.Is(test.err, cause) {
				t.Fatal("execution scope hid the diagnostic cause")
			}
		})
	}
	if boundary, ok := errors.AsType[*chat.CallFailure](failed); !ok || boundary != failed || !errors.Is(failed, inner) {
		t.Fatal("diagnostic inspection lost the composed failure chain")
	}
}

type emptyFailureJoin struct{}

func (emptyFailureJoin) Error() string   { return "unowned diagnostic" }
func (emptyFailureJoin) Unwrap() []error { return nil }

type nilFailureCause struct{}

func (*nilFailureCause) Error() string { panic("typed nil cause must not be called") }

func TestInvalidCallFailureNeverEstablishesExecutionEvidence(t *testing.T) {
	var nilBoundary *chat.CallFailure
	var nilCause *nilFailureCause
	for _, failure := range []*chat.CallFailure{
		nilBoundary,
		{},
		{Kind: "unsupported", Cause: errors.New("cause")},
		{Kind: chat.CallFailureNotExecuted},
		{Kind: chat.CallFailureFailed, Cause: nilCause},
	} {
		if !errors.Is(failure.Validate(), chat.ErrInvalidCallFailure) || chat.ClassifyFailure(failure) != chat.CallFailureUnknown {
			t.Fatalf("invalid boundary established a result: %#v", failure)
		}
		if failure.Error() != chat.ErrInvalidCallFailure.Error() {
			t.Fatal("invalid boundary diagnostic changed")
		}
	}
	if got := chat.ClassifyFailure(fmt.Errorf("wrapper: %w", nilCause)); got != chat.CallFailureUnknown {
		t.Fatalf("wrapped typed nil=%s", got)
	}
	if nilBoundary.Unwrap() != nil || (&chat.CallFailure{Kind: chat.CallFailureFailed, Cause: nilCause}).Unwrap() != nil {
		t.Fatal("invalid nil cause escaped the failure boundary")
	}
}
