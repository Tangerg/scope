package safeguard_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/safeguard"
)

func TestSafeCompletedGenerationSurvivesLateFailure(t *testing.T) {
	failure := errors.New("transport failed after completed generation")
	for _, splitUsage := range []bool{false, true} {
		for _, incremental := range []bool{false, true} {
			name := "whole text"
			if incremental {
				name = "incremental"
			}
			if splitUsage {
				name += "/separate terminal"
			} else {
				name += "/combined terminal"
			}
			t.Run(name, func(t *testing.T) {
				substring, err := safeguard.NewSubstringMatcher(safeguard.SubstringMatcherConfig{Terms: []string{"secret"}})
				if err != nil {
					t.Fatal(err)
				}
				var matcher safeguard.Matcher = substring
				if !incremental {
					matcher = safeguard.MatcherFunc(substring.Match)
				}
				middleware, err := safeguard.NewMiddleware(safeguard.MiddlewareConfig{Matcher: matcher, Scope: safeguard.ScopeOutput})
				if err != nil {
					t.Fatal(err)
				}
				starts, closes := 0, 0
				source := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
					return func(yield func(*chat.ResponseDelta, error) bool) {
						starts++
						defer func() { closes++ }()
						usage := &chat.ResponseMetadata{ID: "generation-123", Usage: &chat.Usage{InputTokens: 4, OutputTokens: 7}}
						terminal := &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("safe completed answer")}, FinishReason: chat.FinishReasonStop}
						if !splitUsage {
							terminal.Metadata = usage
						} else {
							terminal.FinishReason = ""
						}
						if !yield(terminal, nil) {
							return
						}
						if splitUsage && !yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop, Metadata: usage}, nil) {
							return
						}
						yield(nil, failure)
					}
				})
				request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("input")))
				if err != nil {
					t.Fatal(err)
				}
				model, err := chat.NewStreamModel(source)
				if err != nil {
					t.Fatal(err)
				}
				plain, plainErr := model.Call(t.Context(), request)
				called, callErr := middleware.Call(model).Call(t.Context(), request)
				streamed, streamErr := chat.Collect(middleware.Stream(source).Stream(t.Context(), request))
				for _, result := range []struct {
					name     string
					response *chat.Response
					err      error
				}{
					{"unwrapped", plain, plainErr}, {"Call", called, callErr}, {"Stream/Collect", streamed, streamErr},
				} {
					t.Logf("%s response=%t cause_preserved=%t", result.name, result.response != nil, errors.Is(result.err, failure))
					if result.err != failure || result.response == nil { //nolint:errorlint // Safe output must preserve the provider's original error object.
						t.Errorf("%s discarded completed evidence or changed error: response=%v error=%v", result.name, result.response, result.err)
						continue
					}
					response := result.response
					if response.Text() != "safe completed answer" || response.Output.FinishReason != chat.FinishReasonStop || response.Metadata == nil || response.Metadata.ID != "generation-123" || response.Metadata.Usage == nil || response.Metadata.Usage.InputTokens != 4 || response.Metadata.Usage.OutputTokens != 7 {
						t.Errorf("%s changed completed generation: %#v", result.name, response)
					}
				}
				if starts != 3 || closes != 3 {
					t.Fatalf("provider starts=%d closes=%d", starts, closes)
				}
			})
		}
	}
}

func TestBufferedCompletionPreservesRejectionAndConsumerStop(t *testing.T) {
	providerErr := errors.New("transport ended after completion")
	source := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("secret")}, FinishReason: chat.FinishReasonStop}, nil) {
				yield(nil, providerErr)
			}
		}
	})
	for _, blocked := range []bool{false, true} {
		matcher := safeguard.MatcherFunc(func(context.Context, string) (safeguard.Match, error) {
			return safeguard.Match{Found: blocked}, nil
		})
		middleware := mustMiddleware(t, matcher, safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput})
		calls := 0
		middleware.Stream(source).Stream(t.Context(), nil)(func(delta *chat.ResponseDelta, err error) bool {
			calls++
			if blocked {
				if delta != nil || !errors.Is(err, providerErr) || !errors.Is(err, safeguard.ErrUnsafeContent) || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
					t.Fatalf("rejected completion: delta=%v error=%v", delta, err)
				}
			} else if delta == nil || err != nil {
				t.Fatalf("consumer stop received a later error: delta=%v error=%v", delta, err)
			}
			return false
		})
		if calls != 1 {
			t.Fatalf("yield called %d times, want one", calls)
		}
	}
}
