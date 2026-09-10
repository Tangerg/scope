package chatclient

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

type callOnly struct {
	call func(context.Context, *chat.Request) (*chat.Response, error)
}

func (c callOnly) Call(ctx context.Context, request *chat.Request) (*chat.Response, error) {
	return c.call(ctx, request)
}

type streamOnly struct {
	stream func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error]
}

func (s streamOnly) Stream(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	return s.stream(ctx, request)
}

type callAndStream struct {
	callOnly
	streamOnly
}

type pointerModel struct{}

func (*pointerModel) Call(context.Context, *chat.Request) (*chat.Response, error) {
	return nil, errors.New("unreachable")
}

type pointerStreamer struct{}

func (*pointerStreamer) Stream(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	return nil
}

func TestNewRejectsInvalidConstruction(t *testing.T) {
	model := callOnly{call: successfulCall}
	var typedNilModel *pointerModel

	tests := []struct {
		name   string
		model  chat.Model
		config Config
		want   error
	}{
		{name: "nil model", want: ErrNilModel},
		{name: "typed nil model", model: typedNilModel, want: ErrNilModel},
		{
			name:  "middleware returns typed nil model",
			model: model,
			config: Config{CallMiddleware: []chat.CallMiddleware{func(chat.Model) chat.Model {
				return typedNilModel
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.model, test.config)
			if err == nil || client.valid() {
				t.Fatalf("New() = (%v, %v), want zero client and error", client, err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("New() error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}
}

func TestCallProtectsCallerRequest(t *testing.T) {
	request, callerMaxTokens := newProtectedCallFixture(t)
	model := callOnly{call: func(_ context.Context, received *chat.Request) (*chat.Response, error) {
		assertResolvedRequest(t, received, request)
		mutateRequestReferences(received)
		return &chat.Response{Metadata: &chat.ResponseMetadata{ID: "response-1"}}, nil
	}}
	client, err := New(model, Config{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Call(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Metadata.ID != "response-1" {
		t.Fatalf("response ID = %q", response.Metadata.ID)
	}
	assertCallerRequestUnchanged(t, request, *callerMaxTokens)
}

func newProtectedCallFixture(t *testing.T) (*chat.Request, *int64) {
	t.Helper()
	inline, err := media.NewBytes("image/png", []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if setErr := inline.Metadata.Set("origin", "caller"); setErr != nil {
		t.Fatal(setErr)
	}
	message := chat.NewUserMessage(chat.NewMediaPart(inline))
	message.Metadata = metadata.Map{}
	if setErr := message.Metadata.Set("turn", 1); setErr != nil {
		t.Fatal(setErr)
	}
	assistant := chat.NewAssistantMessage(
		chat.NewReasoningPart("thinking", []byte{4, 5}),
		chat.NewToolCallPart(chat.ToolCall{ID: "call-1", Name: "weather", Arguments: `{}`}),
	)
	toolMessage := chat.NewToolMessage(chat.ToolResult{ID: "call-1", Name: "weather", Output: chat.NewTextToolOutput("sunny")})

	requestMaxTokens := int64(7)
	temperature := 0.25
	topP := 0.8
	request := &chat.Request{
		Messages: []chat.Message{message, assistant, toolMessage},
		Tools: []chat.ToolDefinition{{
			Name:        "weather",
			InputSchema: []byte(`{"type":"object"}`),
		}},
		Options: chat.Options{
			Model:           "request-model",
			MaxOutputTokens: &requestMaxTokens,
			Stop:            []string{},
			Temperature:     &temperature,
			TopP:            &topP,
		},
	}
	if setErr := request.Options.Extensions.Set("test/value", "caller"); setErr != nil {
		t.Fatal(setErr)
	}

	return request, &requestMaxTokens
}

func assertResolvedRequest(t *testing.T, received, original *chat.Request) {
	t.Helper()
	if received == original {
		t.Fatal("model received caller-owned request pointer")
	}
	if received.Options.Model != "request-model" {
		t.Fatalf("model = %q, want request-model", received.Options.Model)
	}
	if received.Options.MaxOutputTokens == nil || *received.Options.MaxOutputTokens != 7 {
		t.Fatalf("max tokens = %v, want 7", received.Options.MaxOutputTokens)
	}
	if received.Options.Temperature == nil || *received.Options.Temperature != 0.25 {
		t.Fatalf("temperature = %v, want snapshotted 0.25", received.Options.Temperature)
	}
	if received.Options.TopP == nil || *received.Options.TopP != 0.8 {
		t.Fatalf("top_p = %v, want inherited 0.8", received.Options.TopP)
	}
	if received.Options.Stop == nil || len(received.Options.Stop) != 0 {
		t.Fatalf("stop = %#v, want explicit non-nil empty override", received.Options.Stop)
	}
}

func mutateRequestReferences(received *chat.Request) {
	received.Messages[0].Metadata["turn"][0] = '9'
	received.Messages[0].Parts[0].Media.Source.Bytes[0] = 9
	received.Messages[0].Parts[0].Media.Metadata["origin"][1] = 'X'
	received.Messages[1].Parts[0].ReasoningState[0] = 9
	received.Messages[1].Parts[1].ToolCall.Name = "mutated"
	received.Messages[2].Parts[0].ToolResult.Output.Content[0].Text = "mutated"
	received.Tools[0].InputSchema[2] = 'X'
	if err := received.Options.Extensions.Set("test/value", "changed"); err != nil {
		panic(err)
	}
	*received.Options.MaxOutputTokens = 8
	*received.Options.Temperature = 2
}

func assertCallerRequestUnchanged(t *testing.T, request *chat.Request, callerMaxTokens int64) {
	t.Helper()
	if got := request.Messages[0].Metadata["turn"]; string(got) != "1" {
		t.Fatalf("caller message metadata mutated: %s", got)
	}
	if got := request.Messages[0].Parts[0].Media.Source.Bytes; !reflect.DeepEqual(got, []byte{1, 2, 3}) {
		t.Fatalf("caller media bytes mutated: %v", got)
	}
	if got := request.Messages[0].Parts[0].Media.Metadata["origin"]; string(got) != `"caller"` {
		t.Fatalf("caller media metadata mutated: %s", got)
	}
	if got := request.Messages[1].Parts[0].ReasoningState; !reflect.DeepEqual(got, []byte{4, 5}) {
		t.Fatalf("caller reasoning signature mutated: %v", got)
	}
	if got := request.Messages[1].Parts[1].ToolCall.Name; got != "weather" {
		t.Fatalf("caller tool call mutated: %s", got)
	}
	if got, _ := request.Messages[2].Parts[0].ToolResult.Output.Text(); got != "sunny" {
		t.Fatalf("caller tool result mutated: %s", got)
	}
	if got := string(request.Tools[0].InputSchema); got != `{"type":"object"}` {
		t.Fatalf("caller schema mutated: %s", got)
	}
	got, found, err := request.Options.Extensions.Decode[string]("test/value")
	if err != nil || !found || got != "caller" {
		t.Fatalf("caller extension = %q, %v, %v", got, found, err)
	}
	if callerMaxTokens != 7 {
		t.Fatalf("caller max tokens mutated: %d", callerMaxTokens)
	}
}

func TestCallRejectsInvalidRequestBeforeModel(t *testing.T) {
	var calls atomic.Int64
	client, err := New(callOnly{call: func(context.Context, *chat.Request) (*chat.Response, error) {
		calls.Add(1)
		return nil, nil
	}}, Config{})

	if err != nil {
		t.Fatal(err)
	}

	response, err := client.Call(context.Background(), &chat.Request{})
	if response != nil || !errors.Is(err, chat.ErrInvalidRequest) {
		t.Fatalf("Call() = (%v, %v), want nil and ErrInvalidRequest", response, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("model called %d times for invalid request", calls.Load())
	}
}

func TestClientForwardsContextCancellationAndErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	model := callAndStream{
		call: func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
			return nil, ctx.Err()
		},
		stream: func(ctx context.Context, _ *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return errorSequence(ctx.Err())
		},
	}
	client, err := New(model, Config{})
	if err != nil {
		t.Fatal(err)
	}

	response, err := client.Call(ctx, textRequest("hello"))
	if response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Call() = (%v, %v), want context.Canceled", response, err)
	}
	count := 0
	streamClient, err := NewStreamClient(model, StreamConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for streamResponse, streamErr := range streamClient.Stream(ctx, textRequest("hello")) {
		count++
		if streamResponse != nil || !errors.Is(streamErr, context.Canceled) {
			t.Fatalf("stream yield = (%v, %v), want context.Canceled", streamResponse, streamErr)
		}
	}
	if count != 1 {
		t.Fatalf("stream yield count = %d, want 1", count)
	}
}

func TestCallMiddlewareOrder(t *testing.T) {
	var events []string
	middleware := func(name string) chat.CallMiddleware {
		return func(next chat.Model) chat.Model {
			return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
				events = append(events, name+":before")
				response, err := next.Call(ctx, request)
				events = append(events, name+":after")
				return response, err
			})
		}
	}
	client, err := New(
		callOnly{call: func(context.Context, *chat.Request) (*chat.Response, error) {
			events = append(events, "model")
			return &chat.Response{}, nil
		}},
		Config{CallMiddleware: []chat.CallMiddleware{middleware("outer"), nil, middleware("inner")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), textRequest("hello")); err != nil {
		t.Fatal(err)
	}
	want := []string{"outer:before", "inner:before", "model", "inner:after", "outer:after"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestStreamSnapshotsRequestAndReleasesOnStop(t *testing.T) {
	released := make(chan struct{})
	var seenText string
	model := callAndStream{
		call: successfulCall,
		stream: func(_ context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) {
				defer close(released)
				seenText = request.Messages[0].Text()
				if !yield(&chat.ResponseDelta{Metadata: &chat.ResponseMetadata{ID: "first"}}, nil) {
					return
				}
				yield(&chat.ResponseDelta{Metadata: &chat.ResponseMetadata{ID: "second"}, FinishReason: chat.FinishReasonStop}, nil)
			}
		},
	}
	client, err := NewStreamClient(model, StreamConfig{})
	if err != nil {
		t.Fatal(err)
	}
	request := textRequest("before")
	sequence := client.Stream(context.Background(), request)
	request.Messages[0].Parts[0].Text = "after"

	count := 0
	for response, streamErr := range sequence {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		if response.Metadata.ID != "first" {
			t.Fatalf("response ID = %q", response.Metadata.ID)
		}
		count++
		break
	}
	if count != 1 || seenText != "before" {
		t.Fatalf("count/text = %d/%q, want 1/before", count, seenText)
	}
	select {
	case <-released:
	default:
		t.Fatal("stream resources were not synchronously released")
	}
}

func TestClientsExposeOnlyTheirRequiredCapabilities(t *testing.T) {
	client, err := New(callOnly{call: successfulCall}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(client).(chat.Streamer); ok {
		t.Fatal("call client advertises streaming")
	}
	streamClient, err := NewStreamClient(streamOnly{stream: func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return oneResponse("stream")
	}}, StreamConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(streamClient).(chat.Model); ok {
		t.Fatal("stream client requires synchronous calls")
	}
}

func TestNewStreamClientRejectsInvalidConstruction(t *testing.T) {
	var typedNil *pointerStreamer
	for _, streamer := range []chat.Streamer{nil, typedNil} {
		if _, err := NewStreamClient(streamer, StreamConfig{}); !errors.Is(err, ErrNilStreamer) {
			t.Fatalf("error = %v, want ErrNilStreamer", err)
		}
	}
	streamer := streamOnly{stream: func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return oneResponse("stream")
	}}
	if _, err := NewStreamClient(streamer, StreamConfig{Middleware: []chat.StreamMiddleware{func(chat.Streamer) chat.Streamer { return typedNil }}}); err == nil {
		t.Fatal("accepted nil middleware result")
	}
}

func TestStreamRejectsInvalidRequestBeforeProvider(t *testing.T) {
	called := false
	client, err := NewStreamClient(streamOnly{stream: func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		called = true
		return oneResponse("unexpected")
	}}, StreamConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*chat.Request{nil, {}} {
		count := 0
		for response, err := range client.Stream(t.Context(), request) {
			count++
			if response != nil || !errors.Is(err, chat.ErrInvalidRequest) {
				t.Fatalf("yield = %v, %v", response, err)
			}
		}
		if count != 1 || called {
			t.Fatalf("count/provider called = %d/%v", count, called)
		}
	}
}

func TestStreamMiddlewareOrder(t *testing.T) {
	var events []string
	middleware := func(name string) chat.StreamMiddleware {
		return func(next chat.Streamer) chat.Streamer {
			return chat.StreamerFunc(func(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				return func(yield func(*chat.ResponseDelta, error) bool) {
					events = append(events, name+":before")
					for response, err := range next.Stream(ctx, request) {
						if !yield(response, err) {
							return
						}
					}
					events = append(events, name+":after")
				}
			})
		}
	}
	streamer := streamOnly{stream: func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			events = append(events, "streamer")
			yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}, nil)
		}
	}}
	client, err := NewStreamClient(
		streamer,
		StreamConfig{
			Middleware: []chat.StreamMiddleware{middleware("outer"), nil, middleware("inner")},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, streamErr := range client.Stream(context.Background(), textRequest("hello")) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
	}
	want := []string{"outer:before", "inner:before", "streamer", "inner:after", "outer:after"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestClientConfigurationIsSafeForConcurrentCalls(t *testing.T) {
	var calls atomic.Int64
	client, err := New(
		callOnly{call: func(_ context.Context, request *chat.Request) (*chat.Response, error) {
			if request.Options.Temperature == nil || *request.Options.Temperature != 0.4 {
				return nil, errors.New("missing request temperature")
			}
			calls.Add(1)
			return &chat.Response{}, nil
		}},
		Config{},
	)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 50
	var wait sync.WaitGroup
	errorsFound := make(chan error, goroutines)
	for range goroutines {
		wait.Go(func() {
			request := textRequest("hello")
			request.Options.Temperature = new(0.4)
			_, callErr := client.Call(context.Background(), request)
			errorsFound <- callErr
		})
	}
	wait.Wait()
	close(errorsFound)
	for callErr := range errorsFound {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	if got := calls.Load(); got != goroutines {
		t.Fatalf("calls = %d, want %d", got, goroutines)
	}
}

func TestNilStreamSequenceBecomesTerminalError(t *testing.T) {
	client, err := NewStreamClient(streamOnly{stream: func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return nil
	}}, StreamConfig{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for response, streamErr := range client.Stream(context.Background(), textRequest("hello")) {
		count++
		if response != nil || !errors.Is(streamErr, errNilStreamSequence) {
			t.Fatalf("yield = (%v, %v)", response, streamErr)
		}
	}
	if count != 1 {
		t.Fatalf("yield count = %d, want 1", count)
	}
}

func successfulCall(context.Context, *chat.Request) (*chat.Response, error) {
	return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}, nil
}

func oneResponse(id string) iter.Seq2[*chat.ResponseDelta, error] {
	return func(yield func(*chat.ResponseDelta, error) bool) {
		yield(&chat.ResponseDelta{Metadata: &chat.ResponseMetadata{ID: id}, FinishReason: chat.FinishReasonStop}, nil)
	}
}

func textRequest(text string) *chat.Request {
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart(text)))
	if err != nil {
		panic(err)
	}
	return request
}
