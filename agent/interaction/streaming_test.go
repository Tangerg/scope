package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"github.com/Tangerg/scope/core/invocation"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func textDelta(text string, finish chat.FinishReason) *chat.ResponseDelta {
	return &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}, FinishReason: finish}
}

func TestStreamingFinalOutputDoesNotDependOnDeltaDelivery(t *testing.T) {
	for _, capacity := range []int{0, 1, 8} {
		deltas := make(chan run.Delta, capacity)
		runtime := conformancetest.Runtime(t, run.Config{Deltas: deltas})
		program := compileTest(t, runtime, Config{Streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) {
				for _, delta := range []*chat.ResponseDelta{textDelta("hel", ""), textDelta("lo", chat.FinishReasonStop)} {
					if !yield(delta, nil) {
						return
					}
				}
			}
		})})
		handle := startInteraction(t, runtime, program)
		output := awaitOutput(t, handle)
		if output.ModelCalls != 1 || output.ModelResponse.Text() != "hello" {
			t.Fatalf("capacity=%d output=%+v", capacity, output)
		}
		if capacity != 0 {
			delta := <-deltas
			projection := mustValue(ParseModelResponseDelta(delta.Payload()))
			if delta.ProcessID() != handle.ID() || projection.ResponseDelta().Text() != "hel" {
				t.Fatal("delta lost its process or model content")
			}
			copy := projection.ResponseDelta()
			copy.Parts[0] = chat.NewTextDelta("mutated")
			if projection.ResponseDelta().Text() != "hel" {
				t.Fatal("delta projection aliases caller mutation")
			}
		}
	}
}

func TestStreamingUnknownOutcomesRetainCompletedGeneration(t *testing.T) {
	for _, row := range []struct {
		name      string
		stream    iter.Seq2[*chat.ResponseDelta, error]
		completed bool
	}{
		{"nil stream", nil, false},
		{"empty stream", func(func(*chat.ResponseDelta, error) bool) {}, false},
		{"nil increment", func(yield func(*chat.ResponseDelta, error) bool) { yield(nil, nil) }, false},
		{"partial then refusal", func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(textDelta("partial", ""), nil) {
				yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("continuation refused")})
			}
		}, false},
		{"completed then nil", func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(textDelta("observed", chat.FinishReasonStop), nil) {
				yield(nil, nil)
			}
		}, true},
		{"completed then invalid tail", func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(textDelta("observed", chat.FinishReasonStop), nil) {
				yield(textDelta("late", ""), nil)
			}
		}, true},
		{"completed then panic", func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(textDelta("observed", chat.FinishReasonStop), nil) {
				panic("provider cleanup failed")
			}
		}, true},
		{"partial then panic", func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(textDelta("partial", ""), nil) {
				panic(&invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("panic is not a refusal acknowledgement")})
			}
		}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			store := run.NewMemoryStore()
			runtime := conformancetest.Runtime(t, run.Config{Store: store})
			calls := 0
			program := compileTest(t, runtime, Config{Streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] { calls++; return row.stream })})
			handle := startInteraction(t, runtime, program)
			var pending run.PendingEffect
			awaitLog(t, store, handle.ID(), func(log run.Log) bool {
				inspection := mustValue(log.Inspect())
				process, _ := inspection.Process(handle.ID())
				if len(process.PendingEffects) == 1 && process.PendingEffects[0].Outcome.Status() == run.SettlementStatusUnknown {
					pending = process.PendingEffects[0]
					return true
				}
				return false
			})
			if pending.Failure != nil {
				t.Fatal("interaction lost its available generation to a runtime failure envelope")
			}
			settlement := mustValue(ModelSettlementFromOutcome(pending.Outcome))
			if settlement.Outcome != ModelOutcomeUnknown || settlement.Failure == "" || (settlement.Response != nil) != row.completed {
				t.Fatalf("generation evidence=%+v", settlement)
			}
			if row.completed && settlement.Response.Text() != "observed" {
				t.Fatal("terminal generation changed after failure")
			}
			resolved := mustValue(SettleModelResult(program, pending.Request, finalResponse("investigated")))
			if err := handle.ResolveUnknownEffect(t.Context(), pending.Request.ID(), resolved); err != nil {
				t.Fatal(err)
			}
			if output := awaitOutput(t, handle); output.ModelResponse.Text() != "investigated" || calls != 1 {
				t.Fatal("resolution repeated streaming or replaced investigation")
			}
		})
	}
}

func TestInvalidModelEvidenceAndDeltaEncodingStayUnknown(t *testing.T) {
	for _, model := range []chat.Model{
		chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return nil, nil }),
		chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return &chat.Response{}, nil }),
		chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { panic("model call panicked") }),
	} {
		backend := dispatcher{model: model}
		response, err := backend.callModelResponse(t.Context(), testInput().request(), nil)
		if response != nil || err == nil || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
			t.Fatalf("invalid model generation became definite: %+v %v", response, err)
		}
	}
	if _, err := encodeModelResponseDelta(nil); err == nil {
		t.Fatal("nil delta encoded")
	}
	for _, raw := range []json.RawMessage{[]byte(`{}`), []byte(`{"response_delta":{}}`)} {
		if _, err := ParseModelResponseDelta(raw); err == nil {
			t.Fatal("empty transport increment admitted")
		}
	}
}
