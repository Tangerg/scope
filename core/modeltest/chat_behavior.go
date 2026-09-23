package modeltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
)

const behaviorTimeout = 5 * time.Second

// Lifecycle observes one in-flight provider request. Started closes after the
// mock has sent any initial stream event; Stopped closes when the request
// context is released and the handler exits.
type Lifecycle struct {
	Started <-chan struct{}
	Stopped <-chan struct{}
}

// CallBehaviorCase supplies an in-flight Call and its provider lifecycle.
type CallBehaviorCase struct {
	Model     chat.Model
	Lifecycle Lifecycle
}

// StreamBehaviorCase supplies an in-flight Stream and its provider lifecycle.
type StreamBehaviorCase struct {
	Streamer  chat.Streamer
	Lifecycle Lifecycle
}

// ChatBehaviorContract exercises lifecycle and terminal-error behavior against
// a provider's real SDK transport. Each factory must return fresh state.
type ChatBehaviorContract struct {
	Request            func(t *testing.T) *chat.Request
	CallCancellation   func(t *testing.T) CallBehaviorCase
	StreamCancellation func(t *testing.T) StreamBehaviorCase
	EarlyStop          func(t *testing.T) StreamBehaviorCase
	FirstError         func(t *testing.T) chat.Streamer
}

// RunChatBehaviorContract checks the shared Call and Stream lifecycle through
// the provider transport boundary.
func RunChatBehaviorContract(t *testing.T, contract ChatBehaviorContract) {
	t.Helper()
	if contract.Request == nil || contract.CallCancellation == nil || contract.StreamCancellation == nil || contract.EarlyStop == nil || contract.FirstError == nil {
		t.Fatal("modeltest.ChatBehaviorContract requires every factory")
	}

	t.Run("call context cancellation", func(t *testing.T) {
		test := contract.CallCancellation(t)
		if test.Model == nil {
			t.Fatal("CallCancellation returned nil Model")
		}
		request := contract.validRequest(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type result struct {
			response *chat.Response
			err      error
		}
		completed := make(chan result, 1)
		go func() {
			response, err := test.Model.Call(ctx, request)
			completed <- result{response: response, err: err}
		}()
		waitSignal(t, test.Lifecycle.Started, "provider request start")
		cancel()
		outcome := waitValue(t, completed, "Call return")
		if outcome.response != nil {
			t.Fatalf("canceled Call response = %#v; want nil", outcome.response)
		}
		assertContextCanceled(t, outcome.err)
		waitSignal(t, test.Lifecycle.Stopped, "provider request stop")
	})

	t.Run("stream context cancellation", func(t *testing.T) {
		test := contract.StreamCancellation(t)
		if test.Streamer == nil {
			t.Fatal("StreamCancellation returned nil Streamer")
		}
		request := contract.validRequest(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan []streamYield, 1)
		go func() {
			completed <- drainStream(test.Streamer.Stream(ctx, request))
		}()
		waitSignal(t, test.Lifecycle.Started, "provider stream start")
		cancel()
		outcome := waitValue(t, completed, "Stream return")
		terminal := assertTerminalError(t, outcome, false)
		assertContextCanceled(t, terminal)
		waitSignal(t, test.Lifecycle.Stopped, "provider stream stop")
	})

	t.Run("caller early stop", func(t *testing.T) {
		test := contract.EarlyStop(t)
		if test.Streamer == nil {
			t.Fatal("EarlyStop returned nil Streamer")
		}
		request := contract.validRequest(t)
		count := 0
		for delta, err := range test.Streamer.Stream(t.Context(), request) {
			if err != nil {
				t.Fatalf("Stream before early stop: %v", err)
			}
			assertResponseDelta(t, delta)
			count++
			break
		}
		if count != 1 {
			t.Fatalf("Stream successes before stop = %d; want 1", count)
		}
		waitSignal(t, test.Lifecycle.Started, "provider stream start")
		waitSignal(t, test.Lifecycle.Stopped, "provider stream stop")
	})

	t.Run("first error terminates", func(t *testing.T) {
		streamer := contract.FirstError(t)
		if streamer == nil {
			t.Fatal("FirstError returned nil Streamer")
		}
		outcome := drainStream(streamer.Stream(t.Context(), contract.validRequest(t)))
		_ = assertTerminalError(t, outcome, true)
	})
}

func (c ChatBehaviorContract) validRequest(t *testing.T) *chat.Request {
	t.Helper()
	request := c.Request(t)
	if request == nil {
		t.Fatal("ChatBehaviorContract.Request returned nil")
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Request.Validate: %v", err)
	}
	return request
}

type streamYield struct {
	delta *chat.ResponseDelta
	err   error
}

func drainStream(sequence func(func(*chat.ResponseDelta, error) bool)) []streamYield {
	var outcome []streamYield
	for delta, err := range sequence {
		outcome = append(outcome, streamYield{delta: delta, err: err})
	}
	return outcome
}

func assertTerminalError(t *testing.T, outcome []streamYield, requireSuccess bool) error {
	t.Helper()
	if len(outcome) == 0 {
		t.Fatal("Stream yielded nothing")
	}
	successes := 0
	errorIndex := -1
	var terminal error
	for i := range outcome {
		yielded := outcome[i]
		switch {
		case yielded.err != nil:
			if yielded.delta != nil {
				t.Fatalf("yield %d returned response delta and error", i)
			}
			if errorIndex >= 0 {
				t.Fatalf("Stream yielded multiple errors at %d and %d", errorIndex, i)
			}
			errorIndex = i
			terminal = yielded.err
		case yielded.delta == nil:
			t.Fatalf("yield %d returned nil response delta without error", i)
		default:
			assertResponseDelta(t, yielded.delta)
			successes++
		}
	}
	if errorIndex < 0 {
		t.Fatal("Stream did not yield a terminal error")
	}
	if errorIndex != len(outcome)-1 {
		t.Fatalf("Stream yielded %d values after its first error", len(outcome)-errorIndex-1)
	}
	if requireSuccess && successes == 0 {
		t.Fatal("Stream yielded no successful response before its error")
	}
	return terminal
}

func assertContextCanceled(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v; want context cancellation identity", err)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	if signal == nil {
		t.Fatalf("%s signal is nil", name)
	}
	select {
	case <-signal:
	case <-time.After(behaviorTimeout):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitValue[T any](t *testing.T, values <-chan T, name string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(behaviorTimeout):
		t.Fatalf("timed out waiting for %s", name)
		var zero T
		return zero
	}
}
