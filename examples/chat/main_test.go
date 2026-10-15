package main

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output, arithmeticModel{}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "42\n" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestCallFailureDoesNotPublishAnAnswer(t *testing.T) {
	remoteError := errors.New("model response unavailable")
	for _, scenario := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{name: "model error", ctx: t.Context(), err: remoteError},
		{name: "cancellation", ctx: canceledContext(t.Context()), err: context.Canceled},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			model := chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
				calls++
				return nil, errors.Join(remoteError, ctx.Err())
			})
			var output bytes.Buffer
			err := run(scenario.ctx, &output, model)
			if !errors.Is(err, scenario.err) || calls != 1 || output.Len() != 0 {
				t.Fatalf("error=%v calls=%d output=%q", err, calls, output.String())
			}
		})
	}
}

func TestInvalidStructuredAnswerIsNotRepaired(t *testing.T) {
	for _, text := range []string{`{"value":"42"}`, `{"value":42,"extra":true}`, `not JSON`} {
		t.Run(text, func(t *testing.T) {
			calls := 0
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				calls++
				if request.Options.OutputFormat == nil || request.Options.OutputFormat.Type != chat.OutputFormatJSON {
					t.Error("structured call did not carry its output contract")
				}
				return &chat.Response{Output: &chat.Output{
					Message: new(chat.NewAssistantMessage(chat.NewTextPart(text))), FinishReason: chat.FinishReasonStop,
				}}, nil
			})
			var output bytes.Buffer
			if err := run(t.Context(), &output, model); !errors.Is(err, chatclient.ErrInvalidOutput) || output.Len() != 0 || calls != 1 {
				t.Fatalf("error=%v calls=%d output=%q", err, calls, output.String())
			}
		})
	}
}

func TestStreamTermination(t *testing.T) {
	remoteError := errors.New("stream interrupted")
	for _, scenario := range []struct {
		name       string
		ctx        context.Context
		failure    error
		stopEarly  bool
		wantParts  []string
		wantError  error
		wantFinish chat.FinishReason
		wantYields int
	}{
		{name: "completed", ctx: t.Context(), wantParts: []string{"first", "last"}, wantFinish: chat.FinishReasonStop, wantYields: 2},
		{name: "early stop", ctx: t.Context(), stopEarly: true, wantParts: []string{"first"}, wantYields: 1},
		{name: "failure", ctx: t.Context(), failure: remoteError, wantParts: []string{"first"}, wantError: remoteError, wantYields: 2},
		{name: "cancellation", ctx: canceledContext(t.Context()), wantError: context.Canceled, wantYields: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			closed, yields := false, 0
			streamer := chat.StreamerFunc(func(ctx context.Context, _ *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				return func(yield func(*chat.ResponseDelta, error) bool) {
					defer func() { closed = true }()
					if err := ctx.Err(); err != nil {
						yields++
						yield(nil, err)
						return
					}
					yields++
					if !yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("first")}}, nil) {
						return
					}
					yields++
					if scenario.failure != nil {
						yield(nil, scenario.failure)
						return
					}
					yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("last")}, FinishReason: chat.FinishReasonStop}, nil)
				}
			})
			client, err := chatclient.NewStreamClient(streamer, chatclient.StreamConfig{})
			if err != nil {
				t.Fatal(err)
			}
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("stream")))
			if err != nil {
				t.Fatal(err)
			}
			var parts []string
			var streamError error
			var finish chat.FinishReason
			for delta, deltaErr := range client.Stream(scenario.ctx, request) {
				if deltaErr != nil {
					streamError = deltaErr
					break
				}
				parts = append(parts, delta.Text())
				finish = delta.FinishReason
				if scenario.stopEarly {
					break
				}
			}
			if !errors.Is(streamError, scenario.wantError) || !slices.Equal(parts, scenario.wantParts) || finish != scenario.wantFinish || !closed || yields != scenario.wantYields {
				t.Fatalf("parts=%q finish=%s error=%v closed=%t yields=%d", parts, finish, streamError, closed, yields)
			}
		})
	}
}

func canceledContext(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	cancel()
	return ctx
}
