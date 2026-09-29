package modeltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/samber/lo"

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

type CallBehaviorCase struct {
	Model     chat.Model
	Lifecycle Lifecycle
}

type StreamBehaviorCase struct {
	Streamer  chat.Streamer
	Lifecycle Lifecycle
}

// Each factory must return fresh state backed by the provider transport.
type ChatBehaviorContract struct {
	Request            func(t *testing.T) *chat.Request
	CallCancellation   func(t *testing.T) CallBehaviorCase
	StreamCancellation func(t *testing.T) StreamBehaviorCase
	EarlyStop          func(t *testing.T) StreamBehaviorCase
	FirstError         func(t *testing.T) chat.Streamer
}

func RunChatBehaviorContract(t *testing.T, contract ChatBehaviorContract) {
	t.Helper()
	if contract.Request == nil || contract.CallCancellation == nil || contract.StreamCancellation == nil || contract.EarlyStop == nil || contract.FirstError == nil {
		t.Fatal("modeltest.ChatBehaviorContract requires every factory")
	}

	t.Run("call context cancellation", contract.runCallCancellation)
	t.Run("stream context cancellation", contract.runStreamCancellation)
	t.Run("caller early stop", contract.runEarlyStop)
	t.Run("first error terminates", contract.runFirstError)
}

func (c ChatBehaviorContract) runCallCancellation(t *testing.T) {
	test := c.CallCancellation(t)
	if lo.IsNil(test.Model) {
		t.Fatal("CallCancellation returned nil Model")
	}
	request := c.validRequest(t)
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
}

func (c ChatBehaviorContract) runStreamCancellation(t *testing.T) {
	test := c.StreamCancellation(t)
	if lo.IsNil(test.Streamer) {
		t.Fatal("StreamCancellation returned nil Streamer")
	}
	request := c.validRequest(t)
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
}

func (c ChatBehaviorContract) runEarlyStop(t *testing.T) {
	test := c.EarlyStop(t)
	if lo.IsNil(test.Streamer) {
		t.Fatal("EarlyStop returned nil Streamer")
	}
	request := c.validRequest(t)
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
}

func (c ChatBehaviorContract) runFirstError(t *testing.T) {
	streamer := c.FirstError(t)
	if lo.IsNil(streamer) {
		t.Fatal("FirstError returned nil Streamer")
	}
	outcome := drainStream(streamer.Stream(t.Context(), c.validRequest(t)))
	_ = assertTerminalError(t, outcome, true)
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
