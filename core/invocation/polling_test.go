package invocation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/invocation"
)

func TestPollingFillsOnlyZeroFields(t *testing.T) {
	defaults := invocation.Polling{Interval: time.Second, Timeout: time.Minute}
	if got := (invocation.Polling{Timeout: time.Hour}).Or(defaults); got != (invocation.Polling{Interval: time.Second, Timeout: time.Hour}) {
		t.Fatalf("Or = %+v", got)
	}
	for _, invalid := range []invocation.Polling{{Interval: -1}, {Timeout: -1}} {
		if err := invalid.Validate(); !errors.Is(err, invocation.ErrInvalidPolling) {
			t.Fatalf("%+v.Validate() = %v, want ErrInvalidPolling", invalid, err)
		}
	}
}

func TestPollFetchesUntilTerminal(t *testing.T) {
	polling := invocation.Polling{Interval: time.Millisecond, Timeout: time.Minute}
	fetches := 0
	got, err := polling.Poll(t.Context(), func(context.Context) (int, bool, error) {
		fetches++
		return fetches, fetches == 3, nil
	})
	if err != nil || got != 3 || fetches != 3 {
		t.Fatalf("Poll = %d, %v after %d fetches; want the third value", got, err, fetches)
	}
}

func TestPollStopsAtTheFirstFailureOrTheTimeout(t *testing.T) {
	failure := errors.New("unrecognized status")
	polling := invocation.Polling{Interval: time.Millisecond, Timeout: time.Minute}
	if _, err := polling.Poll(t.Context(), func(context.Context) (int, bool, error) { return 0, false, failure }); !errors.Is(err, failure) {
		t.Fatalf("Poll = %v, want the fetch failure", err)
	}
	brief := invocation.Polling{Interval: time.Millisecond, Timeout: 20 * time.Millisecond}
	if _, err := brief.Poll(t.Context(), func(context.Context) (int, bool, error) { return 0, false, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Poll = %v, want the polling deadline", err)
	}
	if _, err := (invocation.Polling{}).Poll(t.Context(), func(context.Context) (int, bool, error) { return 0, true, nil }); !errors.Is(err, invocation.ErrInvalidPolling) {
		t.Fatalf("unresolved Poll = %v, want ErrInvalidPolling", err)
	}
}
