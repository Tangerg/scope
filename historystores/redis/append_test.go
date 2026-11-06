package redis

import (
	"context"
	"errors"
	"reflect"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

type appendReplyClient struct {
	status    int64
	failure   error
	calls     int
	keys      []string
	arguments []any
}

func (a *appendReplyClient) Eval(ctx context.Context, _ string, keys []string, arguments ...any) *goredis.Cmd {
	a.calls++
	a.keys, a.arguments = keys, arguments
	result := goredis.NewCmd(ctx)
	result.SetVal(a.status)
	result.SetErr(a.failure)
	return result
}

func TestAppendProjectsOnlyAcknowledgedReceipts(t *testing.T) {
	batch, err := history.NewBatch("conversation", "append", chat.NewUserMessage(chat.NewTextPart("message")))
	if err != nil {
		t.Fatal(err)
	}
	transportFailure := errors.New("reply unavailable")
	for _, test := range []struct {
		name             string
		status           int64
		failure, wantErr error
	}{
		{name: "accepted", status: scriptAccepted},
		{name: "conflict", status: scriptConflict, wantErr: history.ErrAppendConflict},
		{name: "closed", status: scriptClosed, wantErr: history.ErrConversationClosed},
		{name: "unresolved", failure: transportFailure, wantErr: transportFailure},
		{name: "invalid acknowledgment", status: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &appendReplyClient{status: test.status, failure: test.failure}
			store, err := NewStore(t.Context(), StoreConfig{Client: client, ScanNodes: func(context.Context, func(context.Context, *goredis.Client) error) error {
				return errors.New("unexpected enumeration")
			}})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.Append(t.Context(), batch)
			if test.status == scriptAccepted {
				if err != nil || receipt.ValidateFor(batch) != nil {
					t.Fatalf("receipt=%+v error=%v", receipt, err)
				}
			} else if receipt != (history.Receipt{}) || err == nil || test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("receipt=%+v error=%v", receipt, err)
			}
			want := []any{operationAppend, batch.ID().String(), batch.Digest(), string(batch.EncodedMessages()), int64(0)}
			if client.calls != 1 || !reflect.DeepEqual(client.keys, store.scriptKeys(batch.ConversationID())) || !reflect.DeepEqual(client.arguments, want) {
				t.Fatalf("calls=%d keys=%v arguments=%v", client.calls, client.keys, client.arguments)
			}
		})
	}
}
