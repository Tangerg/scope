package safeguard_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/safeguard"
)

func TestSubstringScansPreserveUnicodeMatchesAcrossEveryRuneBoundary(t *testing.T) {
	for _, test := range []struct{ term, text string }{
		{"secret", "before SECRET after"},
		{"Straße", "before STRASSE after"},
		{"strasse", "before Straße after"},
		{"κόσμος", "before ΚΌΣΜΟΣ after"},
		{"秘密", "before 秘密 after"},
	} {
		matcher := mustSubstring(t, test.term)
		runes := []rune(test.text)
		for boundary := range len(runes) + 1 {
			scan := matcher.NewScan()
			if _, err := scan.Add(t.Context(), string(runes[:boundary])); err != nil {
				t.Fatal(err)
			}
			match, err := scan.Add(t.Context(), string(runes[boundary:]))
			if err != nil || !match.Found || match.Term != test.term {
				t.Fatalf("term %q split at %d: %+v, %v", test.term, boundary, match, err)
			}
		}
	}
}

func TestCompleteTextMatcherScreensOutputOnceBeforeExposure(t *testing.T) {
	for _, chunks := range []int{1024, 2048, 4096} {
		t.Run(fmt.Sprint(chunks), func(t *testing.T) {
			const fragment = "0123456789abcdef0123456789abcdef"
			calls, screenedBytes := 0, 0
			matcher := safeguard.MatcherFunc(func(_ context.Context, text string) (safeguard.Match, error) {
				calls++
				screenedBytes += len(text)
				return safeguard.Match{}, nil
			})
			middleware := mustMiddleware(t, matcher, safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput})
			received := 0
			for delta, err := range middleware.Stream(repeatedText(chunks, fragment)).Stream(t.Context(), nil) {
				if err != nil || delta == nil || calls != 1 {
					t.Fatalf("unscreened output or failure: calls=%d delta=%v err=%v", calls, delta, err)
				}
				received++
			}
			if calls != 1 || screenedBytes != chunks*len(fragment) || received != chunks+1 {
				t.Fatalf("calls=%d bytes=%d chunks=%d", calls, screenedBytes, received)
			}
		})
	}
}

func TestBufferedOutputIsDiscardedOnPolicyOrProviderFailure(t *testing.T) {
	providerFailure := errors.New("provider interrupted")
	for _, failure := range []error{nil, providerFailure} {
		calls := 0
		matcher := safeguard.MatcherFunc(func(_ context.Context, text string) (safeguard.Match, error) {
			calls++
			return safeguard.Match{Found: strings.Contains(text, "secret")}, nil
		})
		middleware := mustMiddleware(t, matcher, safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput})
		closed := false
		source := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) {
				defer func() { closed = true }()
				for _, text := range []string{"se", "cret"} {
					if !yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}}, nil) {
						return
					}
				}
				if failure != nil {
					yield(nil, failure)
					return
				}
				yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}, nil)
			}
		})
		var got error
		for delta, err := range middleware.Stream(source).Stream(t.Context(), nil) {
			if delta != nil {
				t.Fatal("unscreened or rejected output escaped")
			}
			got = err
		}
		want, wantCalls := safeguard.ErrUnsafeContent, 1
		if failure != nil {
			want, wantCalls = failure, 0
		}
		if !errors.Is(got, want) || !closed || calls != wantCalls {
			t.Fatalf("error=%v closed=%t calls=%d", got, closed, calls)
		}
	}
}

func repeatedText(chunks int, fragment string) chat.Streamer {
	return chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			for range chunks {
				if !yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(fragment)}}, nil) {
					return
				}
			}
			yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}, nil)
		}
	})
}

func BenchmarkOutputScreening(b *testing.B) {
	for _, chunks := range []int{1024, 2048, 4096} {
		for _, incremental := range []bool{false, true} {
			b.Run(fmt.Sprintf("incremental_%t/chunks_%d", incremental, chunks), func(b *testing.B) {
				var matcher safeguard.Matcher = safeguard.MatcherFunc(func(context.Context, string) (safeguard.Match, error) { return safeguard.Match{}, nil })
				if incremental {
					var err error
					matcher, err = safeguard.NewSubstringMatcher(safeguard.SubstringMatcherConfig{Terms: []string{"secret"}})
					if err != nil {
						b.Fatal(err)
					}
				}
				middleware, err := safeguard.NewMiddleware(safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput, Matcher: matcher})
				if err != nil {
					b.Fatal(err)
				}
				const fragment = "0123456789abcdef0123456789abcdef"
				stream := middleware.Stream(repeatedText(chunks, fragment))
				b.ReportAllocs()
				b.SetBytes(int64(chunks * len(fragment)))
				b.ResetTimer()
				for b.Loop() {
					for _, err := range stream.Stream(b.Context(), nil) {
						if err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
