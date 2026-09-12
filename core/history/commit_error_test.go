package history_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

type outcomeStore struct {
	outcome history.WriteOutcome
	err     error
	saved   []chat.Message
}

func (o *outcomeStore) Read(context.Context, history.ConversationID) ([]chat.Message, error) {
	return []chat.Message{}, nil
}

func (o *outcomeStore) Write(_ context.Context, _ history.ConversationID, messages ...chat.Message) (history.WriteOutcome, error) {
	if o.err != nil {
		o.saved = append(o.saved, cloneMessages(messages[:o.outcome.Accepted])...)
		return o.outcome, o.err
	}
	o.saved = append(o.saved, cloneMessages(messages)...)
	return history.WriteOutcome{Accepted: len(messages)}, nil
}

func TestCommitErrorPreservesCompletedGenerationAndWriteFacts(t *testing.T) {
	for _, outcome := range []history.WriteOutcome{{}, {Accepted: 1}, {Uncertain: true}, {Accepted: 1, Uncertain: true}, {Accepted: 2}} {
		for _, streaming := range []bool{false, true} {
			cause := errors.New("persistence failed")
			store := &outcomeStore{outcome: outcome, err: cause}
			middleware, err := history.NewMiddleware(store)
			if err != nil {
				t.Fatal(err)
			}
			ctx := history.WithConversationID(t.Context(), "conversation")
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			terminal := false
			if streaming {
				streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
					return func(yield func(*chat.ResponseDelta, error) bool) {
						calls++
						yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("answer")}, FinishReason: chat.FinishReasonStop}, nil)
					}
				})
				for delta, streamErr := range middleware.Stream(streamer).Stream(ctx, request) {
					if delta != nil && delta.FinishReason == chat.FinishReasonStop {
						terminal = true
					}
					if streamErr != nil {
						if !terminal {
							t.Fatal("commit error preceded generation")
						}
						err = streamErr
					}
				}
			} else {
				response, callErr := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					calls++
					return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("answer"))), FinishReason: chat.FinishReasonStop}}, nil
				})).Call(ctx, request)
				if response.Text() != "answer" {
					t.Fatal("completed response lost")
				}
				err = callErr
			}
			failure, ok := errors.AsType[*history.CommitError](err)
			if !ok || !errors.Is(err, cause) || failure.ConversationID() != "conversation" || failure.Outcome() != outcome {
				t.Fatalf("failure=%#v error=%v", failure, err)
			}
			want := []chat.Message{chat.NewUserMessage(chat.NewTextPart("question")), chat.NewAssistantMessage(chat.NewTextPart("answer"))}
			if !reflect.DeepEqual(failure.Messages(), want) {
				t.Fatalf("messages = %#v", failure.Messages())
			}
			snapshot := failure.Messages()
			snapshot[0].Parts[0].Text = "mutated"
			request.Messages[0].Parts[0].Text = "mutated"
			if !reflect.DeepEqual(failure.Messages(), want) {
				t.Fatal("commit error retained mutable caller state")
			}
			if !outcome.Uncertain {
				store.err = nil
				if _, saveErr := store.Write(ctx, failure.ConversationID(), failure.Messages()[outcome.Accepted:]...); saveErr != nil {
					t.Fatal(saveErr)
				}
				if !reflect.DeepEqual(store.saved, want) {
					t.Fatalf("recovery duplicated or lost messages: %#v", store.saved)
				}
			}
			if calls != 1 {
				t.Fatalf("generation ran %d times", calls)
			}
		}
	}
}

func TestWriteOutcomeRejectsContradictoryFacts(t *testing.T) {
	cause := errors.New("write failed")
	for _, test := range []struct {
		outcome history.WriteOutcome
		count   int
		err     error
	}{
		{history.WriteOutcome{Accepted: -1}, 1, cause},
		{history.WriteOutcome{Accepted: 2}, 1, cause},
		{history.WriteOutcome{}, -1, cause},
		{history.WriteOutcome{Accepted: 1, Uncertain: true}, 1, cause},
		{history.WriteOutcome{}, 1, nil},
		{history.WriteOutcome{Uncertain: true}, 1, nil},
	} {
		if err := test.outcome.Validate(test.count, test.err); !errors.Is(err, history.ErrInvalidWriteOutcome) {
			t.Fatalf("Validate(%#v) = %v", test, err)
		}
	}
	if err := (history.WriteOutcome{Accepted: 2}).Validate(2, nil); err != nil {
		t.Fatal(err)
	}
	if err := (history.WriteOutcome{}).Validate(0, nil); err != nil {
		t.Fatal(err)
	}
}

type contradictoryStore struct{ outcomeStore }

func (c *contradictoryStore) Write(context.Context, history.ConversationID, ...chat.Message) (history.WriteOutcome, error) {
	return history.WriteOutcome{}, nil
}

func TestCommitRejectsUnacknowledgedSuccess(t *testing.T) {
	middleware, err := history.NewMiddleware(&contradictoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	response := &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("answer"))), FinishReason: chat.FinishReasonStop}}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return response, nil })
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := middleware.Call(model).Call(history.WithConversationID(t.Context(), "conversation"), request)
	failure, ok := errors.AsType[*history.CommitError](err)
	if got != response || !ok || !errors.Is(err, history.ErrInvalidWriteOutcome) || failure.Outcome() != (history.WriteOutcome{Uncertain: true}) {
		t.Fatalf("response=%p failure=%#v error=%v", got, failure, err)
	}
}
