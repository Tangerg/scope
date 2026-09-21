package safeguard_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient/safeguard"
)

func TestScopeContract(t *testing.T) {
	for _, scope := range []safeguard.Scope{
		safeguard.ScopeInput,
		safeguard.ScopeOutput,
		safeguard.ScopeBoth,
	} {
		if !scope.Valid() {
			t.Fatalf("scope %q is invalid", scope)
		}
	}
	invalid := safeguard.Scope("unknown")
	if invalid.Valid() {
		t.Fatalf("invalid scope %q reported valid", invalid)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	if _, err := safeguard.NewMiddleware(nil, safeguard.MiddlewareConfig{}); !errors.Is(err, safeguard.ErrInvalidMiddlewareConfig) {
		t.Fatalf("nil matcher error = %v", err)
	}
	var matcherPointer *typedNilMatcher
	if _, err := safeguard.NewMiddleware(matcherPointer, safeguard.MiddlewareConfig{}); !errors.Is(err, safeguard.ErrInvalidMiddlewareConfig) {
		t.Fatalf("typed nil matcher error = %v", err)
	}
	matcher := safeguard.MatcherFunc(func(context.Context, string) (safeguard.Match, error) {
		return safeguard.Match{}, nil
	})
	if _, err := safeguard.NewMiddleware(matcher, safeguard.MiddlewareConfig{Scope: "unknown"}); !errors.Is(err, safeguard.ErrInvalidMiddlewareConfig) {
		t.Fatalf("invalid scope error = %v", err)
	}
	if _, err := safeguard.NewSubstringMatcher([]string{"", "  "}, safeguard.SubstringConfig{}); !errors.Is(err, safeguard.ErrInvalidSubstringConfig) {
		t.Fatalf("empty terms error = %v", err)
	}
}

type typedNilMatcher struct{}

func (*typedNilMatcher) Match(context.Context, string) (safeguard.Match, error) {
	return safeguard.Match{}, nil
}

func TestSubstringMatcherSnapshotsTermsAndSupportsDisclosurePolicy(t *testing.T) {
	terms := []string{" SECRET ", "secret", "other"}
	matcher, err := safeguard.NewSubstringMatcher(terms, safeguard.SubstringConfig{})
	if err != nil {
		t.Fatal(err)
	}
	terms[0] = "mutated"
	match, err := matcher.Match(t.Context(), "contains secret")
	if err != nil || !match.Found || match.Term != "SECRET" {
		t.Fatalf("case-insensitive match = %#v, %v", match, err)
	}
	if match, matchErr := matcher.Match(t.Context(), "clean"); matchErr != nil || match.Found {
		t.Fatalf("clean match = %#v, %v", match, matchErr)
	}

	sensitive, err := safeguard.NewSubstringMatcher([]string{"SECRET"}, safeguard.SubstringConfig{CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	if match, _ := sensitive.Match(t.Context(), "secret"); match.Found {
		t.Fatal("case-sensitive matcher accepted lowercase text")
	}
	hidden, err := safeguard.NewSubstringMatcher([]string{"secret"}, safeguard.SubstringConfig{HideMatch: true})
	if err != nil {
		t.Fatal(err)
	}
	if match, _ := hidden.Match(t.Context(), "secret"); !match.Found || match.Term != "" {
		t.Fatalf("hidden match = %#v", match)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := matcher.Match(ctx, "secret"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestSubstringMatcherUnicodeCaseFolding(t *testing.T) {
	for _, test := range []struct {
		name, term, text string
		want             bool
	}{
		{name: "final sigma", term: "κόσμος", text: "Ο ΚΌΣΜΟΣ", want: true},
		{name: "medial sigma", term: "σ", text: "ς", want: true},
		{name: "sharp s", term: "straße", text: "DIE STRASSE", want: true},
		{name: "reverse expansion", term: "SS", text: "ß", want: true},
		{name: "ligature", term: "office", text: "oﬃce", want: true},
		{name: "distinct text", term: "κόσμος", text: "κόσμημα"},
		{name: "no normalization", term: "é", text: "e\u0301"},
	} {
		t.Run(test.name, func(t *testing.T) {
			matcher := mustSubstring(t, test.term)
			match, err := matcher.Match(t.Context(), test.text)
			want := safeguard.Match{}
			if test.want {
				want = safeguard.Match{Found: true, Term: test.term}
			}
			if err != nil || match != want {
				t.Fatalf("Match(%q) = %#v, %v; want %#v", test.text, match, err, want)
			}
		})
	}

	for _, config := range []safeguard.SubstringConfig{
		{}, {CaseSensitive: true}, {HideMatch: true},
	} {
		matcher, err := safeguard.NewSubstringMatcher([]string{" Straße ", "STRASSE"}, config)
		if err != nil {
			t.Fatal(err)
		}
		want := safeguard.Match{Found: true, Term: "Straße"}
		if config.CaseSensitive {
			want.Term = "STRASSE"
		}
		if config.HideMatch {
			want.Term = ""
		}
		for range 8 {
			t.Run("shared matcher", func(t *testing.T) {
				t.Parallel()
				if got, matchErr := matcher.Match(t.Context(), "STRASSE"); matchErr != nil || got != want {
					t.Fatalf("Match = %#v, %v; want %#v", got, matchErr, want)
				}
			})
		}
	}
}

func TestCallBlocksInputBeforeModelAndReportsBlock(t *testing.T) {
	matcher := mustSubstring(t, "secret")
	var blocks []safeguard.Block
	middleware := mustMiddleware(t, matcher, safeguard.MiddlewareConfig{
		Scope: safeguard.ScopeInput,
		OnBlock: func(_ context.Context, block safeguard.Block) {
			blocks = append(blocks, block)
		},
	})
	called := false
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		called = true
		return response("unused"), nil
	})
	request := mustRequest(t,
		chat.NewSystemMessage("rules"),
		chat.NewUserMessage(chat.NewTextPart("show the SECRET")),
	)
	got, err := middleware.Call(model).Call(t.Context(), request)
	if got != nil || !errors.Is(err, safeguard.ErrUnsafeContent) {
		t.Fatalf("Call result = %#v, %v", got, err)
	}
	var unsafe *safeguard.UnsafeError
	if !errors.As(err, &unsafe) || unsafe.Block.Scope != safeguard.ScopeInput || unsafe.Block.Term != "secret" {
		t.Fatalf("unsafe error = %#v", unsafe)
	}
	if called || !reflect.DeepEqual(blocks, []safeguard.Block{{Scope: safeguard.ScopeInput, Term: "secret"}}) {
		t.Fatalf("called=%v blocks=%#v", called, blocks)
	}
}

func TestCallScansOutputResultBeforeDisclosure(t *testing.T) {
	middleware := mustMiddleware(t, mustSubstring(t, "blocked"), safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput})
	modelResponse := response("result is blocked")
	got, err := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return modelResponse, nil
	})).Call(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("hello"))))
	if got != nil || !errors.Is(err, safeguard.ErrUnsafeContent) {
		t.Fatalf("output block result = %#v, %v", got, err)
	}
}

func TestCallHonorsScopeAndPreservesModelAndMatcherErrors(t *testing.T) {
	request := mustRequest(t, chat.NewUserMessage(chat.NewTextPart("secret input")))
	outputOnly := mustMiddleware(t, mustSubstring(t, "secret"), safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput})
	if got, err := outputOnly.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return response("clean"), nil
	})).Call(t.Context(), request); err != nil || got.Text() != "clean" {
		t.Fatalf("output-only result = %#v, %v", got, err)
	}

	modelErr := errors.New("model failed")
	modelResponse := response("partial")
	got, err := outputOnly.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return modelResponse, modelErr
	})).Call(t.Context(), request)
	if got != modelResponse || !errors.Is(err, modelErr) {
		t.Fatalf("model error result = %p, %v", got, err)
	}

	matchErr := errors.New("matcher failed")
	failing := safeguard.MatcherFunc(func(context.Context, string) (safeguard.Match, error) {
		return safeguard.Match{}, matchErr
	})
	middleware := mustMiddleware(t, failing, safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput})
	got, err = middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return response("answer"), nil
	})).Call(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("hello"))))
	if got != nil || !errors.Is(err, matchErr) {
		t.Fatalf("matcher error result = %#v, %v", got, err)
	}
}

func TestCallInputIgnoresPriorAssistantAndToolMessages(t *testing.T) {
	middleware := mustMiddleware(t, mustSubstring(t, "secret"), safeguard.MiddlewareConfig{Scope: safeguard.ScopeInput})
	request := mustRequest(t,
		chat.NewAssistantMessage(chat.NewTextPart("secret")),
		chat.NewToolMessage(chat.ToolResult{ID: "call-1", Name: "lookup", Output: chat.NewTextToolOutput("secret")}),
		chat.NewUserMessage(chat.NewTextPart("clean")),
	)
	if got, err := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return response("answer"), nil
	})).Call(t.Context(), request); err != nil || got.Text() != "answer" {
		t.Fatalf("Call result = %#v, %v", got, err)
	}
}

func TestStreamDetectsMatchesSplitAcrossChunksBeforeYieldingTrigger(t *testing.T) {
	middleware := mustMiddleware(t, mustSubstring(t, "secret"), safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput})
	closed := false
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			defer func() { closed = true }()
			if !yield(chunk("se"), nil) {
				return
			}
			yield(chunk("cret"), nil)
		}
	})
	var texts []string
	var gotErr error
	for response, err := range middleware.Stream(streamer).Stream(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("hello")))) {
		if err != nil {
			gotErr = err
			continue
		}
		texts = append(texts, response.Text())
	}
	if !closed || !reflect.DeepEqual(texts, []string{"se"}) || !errors.Is(gotErr, safeguard.ErrUnsafeContent) {
		t.Fatalf("stream result closed=%v texts=%v error=%v", closed, texts, gotErr)
	}
}

func TestStreamRejectsUnsafeInputWithoutStartingProvider(t *testing.T) {
	middleware := mustMiddleware(t, mustSubstring(t, "secret"), safeguard.MiddlewareConfig{})
	started := false
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		started = true
		return func(func(*chat.ResponseDelta, error) bool) {}
	})
	var gotErr error
	for _, err := range middleware.Stream(streamer).Stream(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("secret")))) {
		gotErr = err
	}
	if started || !errors.Is(gotErr, safeguard.ErrUnsafeContent) {
		t.Fatalf("stream started=%v error=%v", started, gotErr)
	}
}

func TestStreamPreservesEarlyStopAndProviderFailures(t *testing.T) {
	t.Run("early stop", func(t *testing.T) {
		middleware := mustMiddleware(t, mustSubstring(t, "secret"), safeguard.MiddlewareConfig{})
		closed := false
		streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) {
				defer func() { closed = true }()
				if !yield(chunk("one"), nil) {
					return
				}
				yield(chunk("two"), nil)
			}
		})
		middleware.Stream(streamer).Stream(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("hello"))))(func(*chat.ResponseDelta, error) bool {
			return false
		})
		if !closed {
			t.Fatal("provider resources were not released")
		}
	})

	t.Run("provider error", func(t *testing.T) {
		providerErr := errors.New("provider failed")
		middleware := mustMiddleware(t, mustSubstring(t, "secret"), safeguard.MiddlewareConfig{})
		streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) { yield(nil, providerErr) }
		})
		var gotErr error
		for _, err := range middleware.Stream(streamer).Stream(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("hello")))) {
			gotErr = err
		}
		if !errors.Is(gotErr, providerErr) {
			t.Fatalf("provider error = %v", gotErr)
		}
	})
}

func TestStreamReportsNilSequenceAndMalformedChunk(t *testing.T) {
	middleware := mustMiddleware(t, mustSubstring(t, "secret"), safeguard.MiddlewareConfig{})
	request := mustRequest(t, chat.NewUserMessage(chat.NewTextPart("hello")))
	nilStreamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] { return nil })
	var gotErr error
	for _, err := range middleware.Stream(nilStreamer).Stream(t.Context(), request) {
		gotErr = err
	}
	if !errors.Is(gotErr, safeguard.ErrNilStream) {
		t.Fatalf("nil stream error = %v", gotErr)
	}

	badStreamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			yield(&chat.ResponseDelta{}, nil)
		}
	})
	gotErr = nil
	for _, err := range middleware.Stream(badStreamer).Stream(t.Context(), request) {
		gotErr = err
	}
	if !errors.Is(gotErr, chat.ErrInvalidResponse) {
		t.Fatalf("malformed chunk error = %v", gotErr)
	}
}

func TestUnsafeErrorNilReceiver(t *testing.T) {
	var unsafe *safeguard.UnsafeError
	if unsafe.Error() != safeguard.ErrUnsafeContent.Error() || !errors.Is(unsafe, safeguard.ErrUnsafeContent) {
		t.Fatalf("nil unsafe error = %q", unsafe.Error())
	}
	hidden := &safeguard.UnsafeError{Block: safeguard.Block{Scope: safeguard.ScopeOutput}}
	if got := hidden.Error(); got != "safeguard: unsafe content: output blocked" {
		t.Fatalf("hidden unsafe error = %q", got)
	}
	disclosed := &safeguard.UnsafeError{Block: safeguard.Block{Scope: safeguard.ScopeInput, Term: "secret"}}
	if got := disclosed.Error(); got != `safeguard: unsafe content: input matched "secret"` {
		t.Fatalf("disclosed unsafe error = %q", got)
	}
}

func mustSubstring(t *testing.T, terms ...string) *safeguard.SubstringMatcher {
	t.Helper()
	matcher, err := safeguard.NewSubstringMatcher(terms, safeguard.SubstringConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return matcher
}

func mustMiddleware(t *testing.T, matcher safeguard.Matcher, config safeguard.MiddlewareConfig) *safeguard.Middleware {
	t.Helper()
	middleware, err := safeguard.NewMiddleware(matcher, config)
	if err != nil {
		t.Fatal(err)
	}
	return middleware
}

func mustRequest(t *testing.T, messages ...chat.Message) *chat.Request {
	t.Helper()
	request, err := chat.NewRequest(messages...)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func response(text string) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}
}

func chunk(text string) *chat.ResponseDelta {
	return &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}}
}

func TestCallScreensUnsafeResponseWithError(t *testing.T) {
	cause := errors.New("commit failed")
	blocks := 0
	middleware := mustMiddleware(t, mustSubstring(t, "blocked"), safeguard.MiddlewareConfig{Scope: safeguard.ScopeOutput, OnBlock: func(context.Context, safeguard.Block) { blocks++ }})
	got, err := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return response("blocked"), cause })).Call(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("hello"))))
	if got != nil || !errors.Is(err, cause) || !errors.Is(err, safeguard.ErrUnsafeContent) || blocks != 1 {
		t.Fatalf("response=%v error=%v blocks=%d", got, err, blocks)
	}
}
