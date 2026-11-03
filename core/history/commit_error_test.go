package history_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
	"github.com/Tangerg/scope/core/invocation"
)

type interruptedStore struct {
	inmemory.Store
	commit      bool
	acknowledge bool
	cause       error
}

func (i *interruptedStore) Append(ctx context.Context, batch history.Batch) (history.Receipt, error) {
	var receipt history.Receipt
	if i.commit {
		var err error
		receipt, err = i.Store.Append(ctx, batch)
		if err != nil {
			return receipt, err
		}
	}
	if !i.acknowledge {
		receipt = history.Receipt{}
	}
	return receipt, i.cause
}

func TestCommitErrorRetriesTheSameCompleteBatchWithoutRegeneration(t *testing.T) {
	for _, test := range []struct {
		name                string
		commit, acknowledge bool
	}{
		{"before commit", false, false}, {"lost acknowledgment", true, false}, {"acknowledged ancillary failure", true, true},
	} {
		for _, streaming := range []bool{false, true} {
			mode := " call"
			if streaming {
				mode = " stream"
			}
			t.Run(test.name+mode, func(t *testing.T) {
				cause := errors.New("persistence interrupted")
				store := &interruptedStore{commit: test.commit, acknowledge: test.acknowledge, cause: cause}
				middleware := mustMiddleware(t, store)
				request := mustRequest(t, chat.NewUserMessage(chat.NewTextPart("question")))
				calls := 0
				var err error
				if streaming {
					terminal := false
					next := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
						return func(yield func(*chat.ResponseDelta, error) bool) {
							calls++
							yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("answer")}, FinishReason: chat.FinishReasonStop}, nil)
						}
					})
					for delta, failure := range middleware.Stream(next).Stream(t.Context(), request) {
						if delta != nil && delta.FinishReason == chat.FinishReasonStop {
							terminal = true
						}
						if failure != nil {
							if !terminal {
								t.Fatal("commit error preceded generation")
							}
							err = failure
						}
					}
				} else {
					generated, failure := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
						calls++
						return response(chat.NewAssistantMessage(chat.NewTextPart("answer"))), nil
					})).Call(t.Context(), request)
					if generated.Text() != "answer" {
						t.Fatal("completed generation was lost")
					}
					err = failure
				}
				failure, ok := errors.AsType[*history.CommitError](err)
				if !ok || !errors.Is(err, cause) || failure.Batch().ConversationID() != "conversation-1" || failure.Batch().ID() == "" {
					t.Fatalf("missing retry identity: %v", err)
				}
				kind := invocation.FailureUnknown
				if test.acknowledge {
					kind = invocation.FailureFailed
					if failure.Receipt().ValidateFor(failure.Batch()) != nil {
						t.Fatal("acknowledged receipt was lost")
					}
				}
				if got := invocation.ClassifyFailure(err); got != kind {
					t.Fatalf("classification=%s want=%s", got, kind)
				}
				want := []chat.Message{chat.NewUserMessage(chat.NewTextPart("question")), chat.NewAssistantMessage(chat.NewTextPart("answer"))}
				request.Messages[0].Parts[0].Text = "mutated"
				messages := failure.Batch().Messages()
				messages[0].Parts[0].Text = "mutated copy"
				if !reflect.DeepEqual(failure.Batch().Messages(), want) {
					t.Fatal("retry batch retained mutable caller state")
				}
				encoded, encodeErr := jsonv2.Marshal(failure.Batch())
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				var retry history.Batch
				if decodeErr := jsonv2.Unmarshal(encoded, &retry); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				for range 2 {
					receipt, retryErr := store.Store.Append(t.Context(), retry)
					if retryErr != nil || receipt.ValidateFor(retry) != nil {
						t.Fatal(receipt, retryErr)
					}
				}
				saved, readErr := store.Read(t.Context(), retry.ConversationID())
				if readErr != nil || !reflect.DeepEqual(saved, want) || calls != 1 {
					t.Fatalf("retry duplicated history or generation: saved=%v calls=%d error=%v", saved, calls, readErr)
				}
			})
		}
	}
}

type contradictoryStore struct{ recordingStore }

func (*contradictoryStore) Append(context.Context, history.Batch) (history.Receipt, error) {
	return history.Receipt{}, nil
}

func TestCommitRejectsUnacknowledgedSuccess(t *testing.T) {
	model := mustMiddleware(t, &contradictoryStore{}).Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return response(chat.NewAssistantMessage(chat.NewTextPart("answer"))), nil
	}))
	generated, err := model.Call(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("question"))))
	failure, ok := errors.AsType[*history.CommitError](err)
	if generated.Text() != "answer" || !ok || !errors.Is(err, history.ErrInvalidReceipt) || failure.Receipt() != (history.Receipt{}) || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
		t.Fatalf("contradictory acknowledgment lost facts: %v", err)
	}
}

type rewritingStore struct{ recordingStore }

func (*rewritingStore) Append(_ context.Context, batch history.Batch) (history.Receipt, error) {
	messages := batch.Messages()
	for index := range messages {
		messages[index].Parts[0].Text = "rewritten"
	}
	encoded := batch.EncodedMessages()
	encoded[0] = 'x'
	return history.Receipt{}, errors.New("store disconnected")
}

func TestWriterCannotRewriteGenerationOrReconciliationEvidence(t *testing.T) {
	model := mustMiddleware(t, new(rewritingStore)).Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return response(chat.NewAssistantMessage(chat.NewTextPart("model answer"))), nil
	}))
	generated, err := model.Call(t.Context(), mustRequest(t, chat.NewUserMessage(chat.NewTextPart("question"))))
	failure, ok := errors.AsType[*history.CommitError](err)
	if !ok || generated.Text() != "model answer" {
		t.Fatalf("writer rewrote completed generation: %v", err)
	}
	assertMessages(t, failure.Batch().Messages(), messageKey{chat.RoleUser, "question"}, messageKey{chat.RoleAssistant, "model answer"})
}
