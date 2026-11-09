package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestModelFailureResponseCannotBypassSettlementBudget(t *testing.T) {
	const limit = 512
	for _, kind := range []invocation.FailureKind{invocation.FailureFailed, invocation.FailureUnknown} {
		t.Run(string(kind), func(t *testing.T) {
			definition := mustValue(newDefinition(Config{Name: "test.failed.response", Description: "Bound failed generation evidence.", MaxResponseBytes: limit}))
			calls := 0
			dispatcher := mustValue(newDispatcher(definition, dispatcherConfig{Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls++
				return finalResponse(strings.Repeat("x", limit)), &invocation.Failure{Kind: kind, Cause: errors.New("postprocessing failed")}
			}))}))
			outcome, err := dispatcher.dispatchModel(t.Context(), &modelCall{ModelCallSequence: 1, Request: testInput()}, nil)
			if !errors.Is(err, ErrModelResponseTooLarge) || outcome.Valid() || calls != 1 {
				t.Fatalf("oversized generation acquired settlement authority: bytes=%d status=%s calls=%d error=%v", len(outcome.Payload().JSON()), outcome.Status(), calls, err)
			}
		})
	}
}

func TestModelFailureBudgetMeasuresCanonicalEvidenceWithoutDroppingUsage(t *testing.T) {
	for _, kind := range []invocation.FailureKind{invocation.FailureFailed, invocation.FailureUnknown} {
		t.Run(string(kind), func(t *testing.T) {
			response := finalResponse(strings.Repeat("<>&\u2028\u2029", 20))
			response.Metadata = &chat.ResponseMetadata{Usage: &chat.Usage{InputTokens: 17, OutputTokens: 5}}
			cause := &invocation.Failure{Kind: kind, Cause: errors.New("postprocessing failed")}
			settle := func(limit int) (run.Settlement, error) {
				definition := mustValue(newDefinition(Config{Name: "test.failure.boundary", Description: "Bound canonical evidence bytes.", MaxResponseBytes: limit}))
				dispatcher := mustValue(newDispatcher(definition, dispatcherConfig{Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return response.Clone(), cause }))}))
				return dispatcher.dispatchModel(t.Context(), &modelCall{ModelCallSequence: 1, Request: testInput()}, nil)
			}
			encoded := mustValue(settle(0))
			for _, offset := range []int{-1, 0, 1} {
				outcome, err := settle(len(encoded.Payload().JSON()) + offset)
				if offset < 0 {
					if !errors.Is(err, ErrModelResponseTooLarge) || !errors.Is(err, cause) || outcome.Valid() {
						t.Fatal("oversized evidence was admitted or its diagnostic was lost", err)
					}
					continue
				}
				if err != nil || !outcome.Payload().Equal(encoded.Payload()) || outcome.Status() != encoded.Status() {
					t.Fatal("budget boundary changed evidence", err)
				}
				observed := mustValue(ModelSettlementFromRecord(run.EffectSettled{Outcome: outcome}))
				if observed.Response == nil || observed.Response.Text() != response.Text() || observed.Response.Metadata.Usage.TotalTokens() != 22 || observed.Failure == "" {
					t.Fatal("admitted evidence lost generation, usage, or failure")
				}
			}
		})
	}
}

func TestOversizedFailedModelEvidenceRequiresResolutionWithoutRepeatingCall(t *testing.T) {
	const limit = 512
	for _, kind := range []invocation.FailureKind{invocation.FailureFailed, invocation.FailureUnknown} {
		t.Run(string(kind), func(t *testing.T) {
			store := run.NewMemoryStore()
			runtime := conformancetest.Runtime(t, run.Config{Store: store})
			var calls atomic.Int32
			program := compileTest(t, runtime, Config{MaxResponseBytes: limit, Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls.Add(1)
				return finalResponse(strings.Repeat("x", limit)), &invocation.Failure{Kind: kind, Cause: errors.New("postprocessing failed")}
			}))})
			handle := startInteraction(t, runtime, program)
			var request run.EffectRequest
			log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
				process, _ := mustValue(log.Inspect()).Process(handle.ID())
				if len(process.PendingEffects) != 1 || process.PendingEffects[0].Outcome.Status() != run.SettlementStatusUnknown {
					return false
				}
				request = process.PendingEffects[0].Request
				return true
			})
			for _, entry := range log.Entries {
				settled, ok := entry.Record.(run.EffectSettled)
				if !ok || settled.ID != request.ID() {
					continue
				}
				if len(settled.Outcome.Payload().JSON()) > limit || settled.Failure == nil || !strings.Contains(settled.Failure.Message(), ErrModelResponseTooLarge.Error()) {
					t.Fatal("runtime admitted oversized evidence or concealed its rejection")
				}
			}
			resolved := mustValue(SettleModelResult(program, request, finalResponse("verified")))
			if err := handle.ResolveUnknownEffect(t.Context(), request.ID(), resolved); err != nil {
				t.Fatal(err)
			}
			if output := awaitOutput(t, handle); output.ModelResponse.Text() != "verified" || calls.Load() != 1 {
				t.Fatal("resolving the rejected evidence repeated the model invocation")
			}
		})
	}
}

func TestModelStreamAdmitsScopeEncodedDeltaBytesBeforeAccumulation(t *testing.T) {
	inline, err := media.NewBytes("image/png", []byte(strings.Repeat("x", 400)))
	if err != nil {
		t.Fatal(err)
	}
	for name, delta := range map[string]*chat.ResponseDelta{
		"many small fragments": {Parts: []chat.PartDelta{chat.NewTextDelta("x")}},
		"escaped text":         {Parts: []chat.PartDelta{chat.NewTextDelta(strings.Repeat("<", 90))}},
		"tool arguments":       {Parts: []chat.PartDelta{chat.NewToolCallDelta(chat.ToolCallDelta{ID: "call", Name: "write", Arguments: strings.Repeat("x", 400)})}},
		"metadata":             {Metadata: &chat.ResponseMetadata{Extra: metadata.Map{"test/large": json.RawMessage(`"` + strings.Repeat("x", 400) + `"`)}}},
		"media":                {Parts: []chat.PartDelta{chat.NewMediaDelta(inline)}},
		"reasoning":            {Parts: []chat.PartDelta{chat.NewReasoningDelta("", []byte(strings.Repeat("x", 400)))}},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := encodeModelResponseDelta(delta)
			if err != nil {
				t.Fatal(err)
			}
			const limit = 512
			produced, observed := 0, 0
			closed := false
			dispatcher := &dispatcher{model: StreamModel(StreamModelConfig{MaxDeltaBytes: limit, Streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				return func(yield func(*chat.ResponseDelta, error) bool) {
					defer func() { closed = true }()
					for range 1000 {
						produced++
						if !yield(delta, nil) {
							return
						}
					}
				}
			})})}
			response, err := dispatcher.callModel(t.Context(), nil, func(json.RawMessage) { observed++ }, new(chat.ResponseAccumulator))
			if response != nil || !errors.Is(err, ErrModelStreamTooLarge) || errors.Is(err, ErrModelResponseTooLarge) || !closed {
				t.Fatalf("response=%+v error=%v producer closed=%v", response, err, closed)
			}
			want := limit / len(encoded)
			if produced != want+1 || observed != want {
				t.Fatalf("produced=%d observed=%d, want %d and %d", produced, observed, want+1, want)
			}
		})
	}
}

func TestModelStreamWithinBudgetMatchesAggregateResponse(t *testing.T) {
	want := &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("hello"))), FinishReason: chat.FinishReasonStop}}
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if !yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("hel")}}, nil) {
				return
			}
			yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("lo")}, FinishReason: chat.FinishReasonStop}, nil)
		}
	})
	for _, dispatcher := range []*dispatcher{
		{model: StreamModel(StreamModelConfig{Streamer: streamer})},
		{model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return want.Clone(), nil }))},
	} {
		got, err := dispatcher.callModel(t.Context(), nil, nil, new(chat.ResponseAccumulator))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("response=%+v error=%v", got, err)
		}
	}
}
