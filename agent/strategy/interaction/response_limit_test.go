package interaction_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

type fixedResponseContext struct{ text string }

func (f fixedResponseContext) ReduceModelContext(context.Context, interaction.ModelInvocation, *chat.Request) ([]chat.Message, error) {
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
		{name: "replacement plus response", text: strings.Repeat("x", 200), replacement: strings.Repeat("y", 200)},
		{name: "replacement exceeds budget before call", text: "unused", replacement: strings.Repeat("\x01", 100), beforeCall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			observer := &responseFactObserver{}
			config := interaction.DispatcherConfig{Observer: observer, MaxResponseBytes: 512, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls.Add(1)
				return textResponse(test.text), nil
			})}
			if test.stream {
				config.Model = nil
				config.Streamer = responseStream(streamTextChunk(test.text, chat.FinishReasonStop))
			}
			if test.replacement != "" {
				config.ModelContextReducer = fixedResponseContext{text: test.replacement}
			}
			deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "interaction.response_limit", Description: "Reject unusable model output.", MaxModelCalls: agent.NewQuota(1)}, config, interaction.ToolSetConfig{})
			events := &agenttest.ObservationRecorder{}
			engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{events}})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(ctx))
			process, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "bounded"))
			if err != nil {
				t.Fatal(err)
			}
			defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
			event, err := events.AwaitEvent(ctx, func(event agent.Event) bool { _, ok := event.EffectFinished(); return ok })
			if err != nil {
				t.Fatal(err)
			}
			fact, _ := event.EffectFinished()
			wantObservations := int32(1)
			if test.stream || test.beforeCall {
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
				if fact.SettlementStatus() != agent.SettlementStatusFailed || calls.Load() != 0 {
					t.Fatalf("status=%s calls=%d", fact.SettlementStatus(), calls.Load())
				}
			} else if fact.SettlementStatus() != agent.SettlementStatusUnknown {
				t.Fatalf("oversize output produced %s, want unknown", fact.SettlementStatus())
			}
		})
	}
}

// Capture the actual settlement returned to Engine, including its canonical bytes.
type responseLimitDispatcher struct {
	*interaction.Dispatcher
	finished chan responseLimitOutcome
}

type responseLimitOutcome struct {
	settlement agent.Settlement
	err        error
}

func (r responseLimitDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	settlement, err := r.Dispatcher.Dispatch(ctx, request, emit)
	r.finished <- responseLimitOutcome{settlement, err}
	return settlement, err
}

func dispatchLimitedResponse(t *testing.T, config interaction.DispatcherConfig) responseLimitOutcome {
	t.Helper()
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "canonical.response", Description: "Measure canonical response settlements.", MaxModelCalls: agent.NewQuota(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, config)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan responseLimitOutcome, 1)
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: responseLimitDispatcher{dispatcher, finished},
		ImplementationDigest: agent.ComputeDigest([]byte("canonical-response")), ConfigurationDigest: agent.ComputeDigest([]byte("config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer engine.Close(context.WithoutCancel(ctx))
	process, err := engine.Start(ctx, deployment, interactionInput(t, "bounded"))
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
				config := interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return textResponse(text), nil })}
				if stream {
					config.Model = nil
					config.Streamer = responseStream(streamTextChunk(text, chat.FinishReasonStop))
				}
				if replacement {
					config.ModelContextReducer = fixedResponseContext{text: text}
				}
				baseline := dispatchLimitedResponse(t, config)
				if baseline.err != nil || baseline.settlement.Status() != agent.SettlementStatusSucceeded {
					t.Fatalf("baseline = %+v", baseline)
				}
				size := len(baseline.settlement.Payload())
				for _, offset := range []int{-1, 0, 1} {
					config.MaxResponseBytes = size + offset
					got := dispatchLimitedResponse(t, config)
					if offset < 0 {
						if !errors.Is(got.err, interaction.ErrModelResponseTooLarge) {
							t.Fatalf("limit=%d error=%v", config.MaxResponseBytes, got.err)
						}
						continue
					}
					if got.err != nil || got.settlement.Status() != agent.SettlementStatusSucceeded || len(got.settlement.Payload()) != size || len(got.settlement.Payload()) > config.MaxResponseBytes {
						t.Fatalf("limit=%d payload=%d status=%s error=%v", config.MaxResponseBytes, len(got.settlement.Payload()), got.settlement.Status(), got.err)
					}
				}
			})
		}
	}
}

func TestResponseAdmissionReservesMinimumCompleteProtocol(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, replacement := range []string{"", strings.Repeat("<>&\u2028\u2029", 20)} {
			// Measure the canonical envelope without a response, then leave one byte.
			base := `{"operation":"model_call","model_result":{}}`
			if replacement != "" {
				messages, err := agent.EncodePayload([]chat.Message{chat.NewUserMessage(chat.NewTextPart(replacement))})
				if err != nil {
					t.Fatal(err)
				}
				base = `{"operation":"model_call","model_result":{"replacement_messages":` + string(messages.JSON()) + `}}`
			}
			for _, limit := range []int{1, len(base) + 1} {
				var calls atomic.Int32
				config := interaction.DispatcherConfig{MaxResponseBytes: limit, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
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
					config.ModelContextReducer = fixedResponseContext{text: replacement}
				}
				got := dispatchLimitedResponse(t, config)
				if calls.Load() != 0 || got.err != nil || got.settlement.Status() != agent.SettlementStatusFailed {
					t.Fatalf("limit=%d calls=%d status=%s error=%v", limit, calls.Load(), got.settlement.Status(), got.err)
				}
				// Diagnostics have a distinct, bounded budget, even for a one-byte response budget.
				if len(got.settlement.Payload()) > agent.MaxPayloadBytes {
					t.Fatal("unbounded host diagnostic")
				}
			}
		}
	}
}

type failingResponseContext struct{ cause error }

func (f failingResponseContext) ReduceModelContext(context.Context, interaction.ModelInvocation, *chat.Request) ([]chat.Message, error) {
	return nil, f.cause
}

func TestResponseHostDiagnosticUsesIndependentBoundedBudget(t *testing.T) {
	var calls atomic.Int32
	got := dispatchLimitedResponse(t, interaction.DispatcherConfig{
		MaxResponseBytes: 1,
		Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			calls.Add(1)
			return textResponse("unused"), nil
		}),
		ModelContextReducer: failingResponseContext{cause: errors.New(strings.Repeat("<", agent.MaxDiagnosticBytes+1))},
	})
	if got.err != nil || got.settlement.Status() != agent.SettlementStatusFailed || calls.Load() != 0 {
		t.Fatalf("host failure status=%s calls=%d error=%v", got.settlement.Status(), calls.Load(), got.err)
	}
	payload, err := agent.ParsePayload(got.settlement.Payload())
	if err != nil {
		t.Fatal(err)
	}
	wire, err := payload.Decode[struct {
		Operation   string `json:"operation"`
		ModelResult struct {
			HostError string `json:"host_error"`
		} `json:"model_result"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	want := "interaction: reduce model context: " + strings.Repeat("<", agent.MaxDiagnosticBytes-len("interaction: reduce model context: "))
	if wire.Operation != "model_call" || wire.ModelResult.HostError != want || len(wire.ModelResult.HostError) != agent.MaxDiagnosticBytes {
		t.Fatalf("diagnostic was not bounded at its owner: operation=%s bytes=%d", wire.Operation, len(wire.ModelResult.HostError))
	}
}

type responseFactObserver struct{ calls atomic.Int32 }

func (r *responseFactObserver) OnModelResponse(_ context.Context, _ interaction.ModelInvocation, response *chat.Response) {
	r.calls.Add(1)
	response.Output = nil
}
