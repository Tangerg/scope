package agent_test

import (
	startrand "crypto/rand"

	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/core/chat"
)

type fixedResponseContext struct{ text string }

func (f fixedResponseContext) PrepareContext(context.Context, agent.ContextInvocation, *chat.Request) ([]chat.Message, error) {
	return []chat.Message{chat.NewUserMessage(chat.NewTextPart(f.text))}, nil
}

func TestModelResponseLimitIncludesEncodingAndReplacementContext(t *testing.T) {
	for _, test := range []struct {
		name, text, replacement string
		stream                  bool
		beforeCall              bool
	}{
		{name: "aggregate escaped text", text: strings.Repeat("\x01", 100)},
		{name: "stream escaped text", text: strings.Repeat("\x01", 100), stream: true},
		{name: "model response exceeds its own budget after preparation", text: strings.Repeat("x", 600), replacement: strings.Repeat("y", 200)},
		{name: "replacement exceeds budget before call", text: "unused", replacement: strings.Repeat("\x01", 100), beforeCall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			observer := &responseMutatingObserver{}
			config := agent.DispatcherConfig{Observer: observer, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls.Add(1)
				return textResponse(test.text), nil
			})}
			if test.stream {
				config.Model = nil
				config.Streamer = responseStream(streamTextChunk(test.text, chat.FinishReasonStop))
			}
			if test.replacement != "" {
				config.ContextProcessor = fixedResponseContext{text: test.replacement}
			}
			deployment := configuredInteraction(t, agent.DefinitionConfig{MaxResponseBytes: 512, Name: "interaction.response_limit", Description: "Reject unusable model output.", MaxModelCalls: run.NewQuota(1)}, config, agent.ToolSetConfig{})
			events := &runtest.ObservationRecorder{}
			engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), EventListeners: []run.EventListener{events}})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(ctx))
			process, err := engine.Start(ctx, deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "bounded")})
			if err != nil {
				t.Fatal(err)
			}
			defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
			event, err := events.AwaitEvent(ctx, func(event run.Event) bool {
				f, ok := event.EffectFinished()
				return ok && f.SettlementStatus() != run.SettlementStatusSucceeded
			})
			if err != nil {
				t.Fatal(err)
			}
			fact, _ := event.EffectFinished()
			wantObservations := int32(1)
			if test.beforeCall {
				wantObservations = 0
			}
			if observer.calls.Load() != wantObservations {
				t.Fatalf("response observations=%d, want %d", observer.calls.Load(), wantObservations)
			}
			if test.beforeCall {
				result, err := process.Await(ctx)
				if err != nil {
					t.Fatal(err)
				}
				assertInteractionHostFailure(t, result)
				if fact.SettlementStatus() != run.SettlementStatusFailed || calls.Load() != 0 {
					t.Fatalf("status=%s calls=%d", fact.SettlementStatus(), calls.Load())
				}
			} else if fact.SettlementStatus() != run.SettlementStatusUnknown {
				t.Fatalf("oversize output produced %s, want unknown", fact.SettlementStatus())
			}
		})
	}
}

type responseLimitDispatcher struct {
	*agent.Dispatcher
	finished chan responseLimitOutcome
}

type responseLimitOutcome struct {
	settlement run.Settlement
	err        error
}

func (r responseLimitDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	settlement, err := r.Dispatcher.Dispatch(ctx, request, emit)
	if err == nil && settlement.Status() == run.SettlementStatusSucceeded {
		var envelope struct {
			Prepared json.RawMessage `json:"prepared_context"`
		}
		if jsonv2.Unmarshal(settlement.Payload(), &envelope) == nil && len(envelope.Prepared) > 0 {
			return settlement, nil
		}
	}
	r.finished <- responseLimitOutcome{settlement, err}
	return settlement, err
}

func dispatchLimitedResponse(t *testing.T, config agent.DispatcherConfig, maxBytes int) responseLimitOutcome {
	t.Helper()
	definition, err := agent.NewDefinition(agent.DefinitionConfig{
		MaxResponseBytes: maxBytes, PrepareContext: config.ContextProcessor != nil, Name: "canonical.response", Description: "Measure canonical response settlements.", MaxModelCalls: run.NewQuota(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := agent.NewDispatcher(definition, config)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan responseLimitOutcome, 1)
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: responseLimitDispatcher{dispatcher, finished},
		ImplementationDigest: run.ComputeDigest([]byte("canonical-response")), ConfigurationDigest: run.ComputeDigest([]byte("config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer engine.Close(context.WithoutCancel(ctx))
	process, err := engine.Start(ctx, deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "bounded")})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
	select {
	case outcome := <-finished:
		return outcome
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return responseLimitOutcome{}
	}
}

func TestResponseLimitMeasuresCanonicalSettlementBoundaries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream_%t/replacement_%t", stream, replacement), func(t *testing.T) {
				text := strings.Repeat("<>&\u2028\u2029", 20)
				config := agent.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return textResponse(text), nil })}
				if stream {
					config.Model = nil
					config.Streamer = responseStream(streamTextChunk(text, chat.FinishReasonStop))
				}
				if replacement {
					config.ContextProcessor = fixedResponseContext{text: "compact"}
				}
				baseline := dispatchLimitedResponse(t, config, 0)
				if baseline.err != nil || baseline.settlement.Status() != run.SettlementStatusSucceeded {
					t.Fatalf("baseline = %+v", baseline)
				}
				size := len(baseline.settlement.Payload())
				for _, offset := range []int{-1, 0, 1} {
					maxBytes := size + offset
					got := dispatchLimitedResponse(t, config, maxBytes)
					if offset < 0 {
						if !errors.Is(got.err, agent.ErrModelResponseTooLarge) {
							t.Fatalf("limit=%d error=%v", maxBytes, got.err)
						}
						continue
					}
					if got.err != nil || got.settlement.Status() != run.SettlementStatusSucceeded || len(got.settlement.Payload()) != size || len(got.settlement.Payload()) > maxBytes {
						t.Fatalf("limit=%d payload=%d status=%s error=%v", maxBytes, len(got.settlement.Payload()), got.settlement.Status(), got.err)
					}
				}
			})
		}
	}
}

func TestResponseLimitIsIndependentOfStreamFragmentation(t *testing.T) {
	const text = "01234567890123456789012345678901"
	const settlementBytes = 164
	var fragments []*chat.ResponseDelta
	for _, character := range strings.Split(text, "") {
		fragments = append(fragments, streamTextChunk(character, ""))
	}
	fragments[len(fragments)-1].FinishReason = chat.FinishReasonStop
	for _, test := range []struct {
		name   string
		config agent.DispatcherConfig
	}{
		{name: "aggregate", config: agent.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return textResponse(text), nil })}},
		{name: "single_delta", config: agent.DispatcherConfig{Streamer: responseStream(streamTextChunk(text, chat.FinishReasonStop))}},
		{name: "one_character_deltas", config: agent.DispatcherConfig{Streamer: responseStream(fragments...)}},
	} {
		for _, limit := range []int{settlementBytes - 1, settlementBytes, settlementBytes + 1} {
			t.Run(fmt.Sprintf("%s/limit_%d", test.name, limit), func(t *testing.T) {
				got := dispatchLimitedResponse(t, test.config, limit)
				if limit < settlementBytes {
					if !errors.Is(got.err, agent.ErrModelResponseTooLarge) || errors.Is(got.err, agent.ErrModelStreamTooLarge) {
						t.Fatalf("final settlement admission error=%v", got.err)
					}
					return
				}
				if got.err != nil || got.settlement.Status() != run.SettlementStatusSucceeded || len(got.settlement.Payload()) != settlementBytes {
					t.Fatalf("status=%s bytes=%d error=%v", got.settlement.Status(), len(got.settlement.Payload()), got.err)
				}
			})
		}
	}
}

func TestStreamAdmissionHasItsOwnBudgetAndFailure(t *testing.T) {
	closed := false
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			defer func() { closed = true }()
			for range 32 {
				if !yield(streamTextChunk("x", ""), nil) {
					return
				}
			}
			yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}, nil)
		}
	})
	got := dispatchLimitedResponse(t, agent.DispatcherConfig{Streamer: streamer, MaxStreamDeltaBytes: 512}, 512)
	if !errors.Is(got.err, agent.ErrModelStreamTooLarge) || errors.Is(got.err, agent.ErrModelResponseTooLarge) || !closed {
		t.Fatalf("stream admission error=%v producer closed=%t", got.err, closed)
	}
}

func TestResponseAdmissionReservesMinimumCompleteProtocol(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, replacement := range []string{"", strings.Repeat("<>&\u2028\u2029", 20)} {
			base := `{"model_result":{}}`
			for _, limit := range []int{1, len(base) + 1} {
				var calls atomic.Int32
				config := agent.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					calls.Add(1)
					return textResponse("unused"), nil
				})}
				if stream {
					config.Model = nil
					config.Streamer = chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
						calls.Add(1)
						return responseStream(streamTextChunk("unused", chat.FinishReasonStop)).Stream(t.Context(), nil)
					})
				}
				if replacement != "" {
					config.ContextProcessor = fixedResponseContext{text: replacement}
				}
				got := dispatchLimitedResponse(t, config, limit)
				if calls.Load() != 0 || got.err != nil || got.settlement.Status() != run.SettlementStatusFailed {
					t.Fatalf("limit=%d calls=%d status=%s error=%v", limit, calls.Load(), got.settlement.Status(), got.err)
				}
				// Diagnostics have a distinct, bounded budget, even for a one-byte response budget.
				if len(got.settlement.Payload()) > run.MaxPayloadBytes {
					t.Fatal("unbounded host diagnostic")
				}
			}
		}
	}
}

type failingResponseContext struct{ cause error }

func (f failingResponseContext) PrepareContext(context.Context, agent.ContextInvocation, *chat.Request) ([]chat.Message, error) {
	return nil, &agent.ContextFailure{Cause: f.cause}
}

func TestResponseHostDiagnosticUsesIndependentBoundedBudget(t *testing.T) {
	var calls atomic.Int32
	got := dispatchLimitedResponse(t, agent.DispatcherConfig{

		Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			calls.Add(1)
			return textResponse("unused"), nil
		}),
		ContextProcessor: failingResponseContext{cause: errors.New(strings.Repeat("<", run.MaxDiagnosticBytes+1))},
	}, 1)
	if got.err != nil || got.settlement.Status() != run.SettlementStatusFailed || calls.Load() != 0 {
		t.Fatalf("host failure status=%s calls=%d error=%v", got.settlement.Status(), calls.Load(), got.err)
	}
	payload, err := run.ParsePayload(got.settlement.Payload())
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := payload.Decode[string]()
	if err != nil {
		t.Fatal(err)
	}
	want := "agent: context preparation failed: " + strings.Repeat("<", run.MaxDiagnosticBytes-len("agent: context preparation failed: "))
	if diagnostic != want || len(diagnostic) != run.MaxDiagnosticBytes {
		t.Fatalf("diagnostic was not bounded at its owner: bytes=%d", len(diagnostic))
	}
}

func TestDefiniteFailureModelErrorSettlesFailedNotUnknown(t *testing.T) {
	for _, definite := range []error{
		&chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("rejected before the provider")},
		&chat.CallFailure{Kind: chat.CallFailureFailed, Cause: errors.New("output rejected after the provider ran")},
	} {
		got := dispatchLimitedResponse(t, agent.DispatcherConfig{
			Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return nil, definite }),
		}, 0)
		if got.err != nil || got.settlement.Status() != run.SettlementStatusFailed {
			t.Fatalf("definite failure %v outcome = %+v, want a definite failed settlement", definite, got)
		}
	}

	unknown := dispatchLimitedResponse(t, agent.DispatcherConfig{
		Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			return nil, errors.New("ambiguous provider failure")
		}),
	}, 0)
	if unknown.err == nil {
		t.Fatalf("plain model error outcome = %+v, want an unknown outcome (non-nil error)", unknown)
	}
}

type responseMutatingObserver struct{ calls atomic.Int32 }

func (*responseMutatingObserver) OnModelStarted(context.Context, agent.ModelInvocation, *chat.Request) {
}

func (r *responseMutatingObserver) OnModelSettled(_ context.Context, _ agent.ModelInvocation, settlement agent.ModelSettlement) {
	if settlement.Response == nil {
		return
	}
	response := settlement.Response
	r.calls.Add(1)
	response.Output = nil
}
