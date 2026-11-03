package chatclient

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
)

func TestClientsOwnPreflightEvidence(t *testing.T) {
	var calls int
	client, err := New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		return nil, errors.New("unexpected model call")
	}))
	if err != nil {
		t.Fatal(err)
	}
	streamClient, err := NewStreamClient(chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		calls++
		return errorSequence(errors.New("unexpected streamer call"))
	}))
	if err != nil {
		t.Fatal(err)
	}
	duplicateFormat := textRequest("hello")
	duplicateFormat.Options.OutputFormat = new(Text().Contract())
	for _, test := range []struct {
		name string
		call func() error
		want error
	}{
		{"nil call request", func() error { _, err := client.Call(t.Context(), nil); return err }, chat.ErrInvalidRequest},
		{"empty call request", func() error { _, err := client.Call(t.Context(), &chat.Request{}); return err }, chat.ErrInvalidRequest},
		{"zero call client", func() error { _, err := (Client{}).Call(t.Context(), textRequest("hello")); return err }, ErrInvalidClient},
		{"nil output request", func() error { _, err := client.Output(t.Context(), nil, Text()); return err }, chat.ErrInvalidRequest},
		{"duplicate output format", func() error { _, err := client.Output(t.Context(), duplicateFormat, Text()); return err }, ErrInvalidOutputFormat},
		{"zero output format", func() error { _, err := client.Output(t.Context(), textRequest("hello"), Format[string]{}); return err }, ErrInvalidOutputFormat},
		{"zero output client", func() error { _, err := (Client{}).Output(t.Context(), textRequest("hello"), Text()); return err }, ErrInvalidClient},
		{"nil stream request", func() error { return streamFailure(t, streamClient.Stream(t.Context(), nil)) }, chat.ErrInvalidRequest},
		{"empty stream request", func() error { return streamFailure(t, streamClient.Stream(t.Context(), &chat.Request{})) }, chat.ErrInvalidRequest},
		{"zero stream client", func() error { return streamFailure(t, (StreamClient{}).Stream(t.Context(), textRequest("hello"))) }, ErrNilStreamer},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := test.call()
			if invocation.ClassifyFailure(failure) != invocation.FailureNotExecuted || !errors.Is(failure, test.want) || calls != 0 {
				t.Fatalf("error=%v calls=%d, want not-executed %v without model work", failure, calls, test.want)
			}
		})
	}
}

func TestClientsPreserveProviderEvidence(t *testing.T) {
	for _, failure := range []error{
		errors.New("provider transport failure"),
		&invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("provider preflight")},
		&invocation.Failure{Kind: invocation.FailureFailed, Cause: errors.New("provider rejection")},
		&invocation.Failure{Kind: invocation.FailureUnknown, Cause: errors.New("provider acknowledgment lost")},
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			var calls int
			client, err := New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls++
				return nil, failure
			}))
			if err != nil {
				t.Fatal(err)
			}
			streamClient, err := NewStreamClient(chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				calls++
				return errorSequence(failure)
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := client.Call(t.Context(), textRequest("hello"))
			_, outputErr := client.Output(t.Context(), textRequest("hello"), Text())
			streamErr := streamFailure(t, streamClient.Stream(t.Context(), textRequest("hello")))
			for _, forwarded := range []error{callErr, outputErr, streamErr} {
				if !errors.Is(forwarded, failure) || invocation.ClassifyFailure(forwarded) != invocation.ClassifyFailure(failure) {
					t.Fatalf("error=%v, want provider evidence %v", forwarded, failure)
				}
			}
			if calls != 3 {
				t.Fatalf("model calls=%d, want 3", calls)
			}
		})
	}
}

func TestOutputDecodeFailureFollowsExecutedModel(t *testing.T) {
	var calls int
	client, err := New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		return responseWithText(t, "not JSON"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Output(t.Context(), textRequest("hello"), JSON[recipe]())
	if calls != 1 || !errors.Is(err, ErrInvalidOutput) || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
		t.Fatalf("error=%v calls=%d, want output failure after one model call", err, calls)
	}
}

func streamFailure(t *testing.T, sequence iter.Seq2[*chat.ResponseDelta, error]) error {
	t.Helper()
	var failure error
	var count int
	for response, err := range sequence {
		if response != nil || err == nil {
			t.Fatalf("response=%v error=%v, want failure without response", response, err)
		}
		count++
		failure = err
	}
	if count != 1 {
		t.Fatalf("stream yielded %d failures, want one", count)
	}
	return failure
}
