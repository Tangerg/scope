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

func TestOutputScreeningRequiresTerminalForEveryMatcher(t *testing.T) {
	incremental, err := safeguard.NewSubstringMatcher(safeguard.SubstringMatcherConfig{Terms: []string{"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	completeText := safeguard.MatcherFunc(func(context.Context, string) (safeguard.Match, error) { return safeguard.Match{}, nil })
	for _, matcher := range []struct {
		name  string
		value safeguard.Matcher
	}{
		{"incremental", incremental}, {"complete text", completeText},
	} {
		for _, text := range []string{"safe prefix", ""} {
			t.Run(matcher.name+"/"+text, func(t *testing.T) {
				middleware, err := safeguard.NewMiddleware(safeguard.MiddlewareConfig{Matcher: matcher.value, Scope: safeguard.ScopeOutput})
				if err != nil {
					t.Fatal(err)
				}
				source := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
					return func(yield func(*chat.ResponseDelta, error) bool) {
						if text != "" {
							yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}}, nil)
						}
					}
				})
				failures := 0
				var terminal error
				for _, err := range middleware.Stream(source).Stream(t.Context(), nil) {
					if err != nil {
						failures++
						terminal = err
					}
				}
				if failures != 1 || !errors.Is(terminal, chat.ErrInvalidResponse) {
					t.Fatalf("unfinished generation: failures=%d error=%v", failures, terminal)
				}
			})
		}
	}
}

type nilScanMatcher struct{}

func (n nilScanMatcher) Match(context.Context, string) (safeguard.Match, error) {
	return safeguard.Match{}, nil
}

func (n nilScanMatcher) NewScan() safeguard.Scan { return (*nilScan)(nil) }

type nilScan struct{}

func (n *nilScan) Add(context.Context, string) (safeguard.Match, error) {
	return safeguard.Match{}, nil
}

func TestTypedNilScanFailsBeforeProviderStarts(t *testing.T) {
	middleware, err := safeguard.NewMiddleware(safeguard.MiddlewareConfig{Matcher: nilScanMatcher{}, Scope: safeguard.ScopeOutput})
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	source := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		starts++
		return func(yield func(*chat.ResponseDelta, error) bool) {
			yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}, nil)
		}
	})
	failures := 0
	for response, err := range middleware.Stream(source).Stream(t.Context(), nil) {
		if response != nil || !errors.Is(err, safeguard.ErrInvalidMiddlewareConfig) || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
			t.Fatalf("response=%v error=%v", response, err)
		}
		failures++
	}
	if failures != 1 || starts != 0 {
		t.Fatalf("failures=%d provider starts=%d", failures, starts)
	}
}
