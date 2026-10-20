package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/redis"
)

type writeClient struct {
	goredis.UniversalClient
	pipeline *writePipeline
}

func (w writeClient) TxPipeline() goredis.Pipeliner { return w.pipeline }
func (w writeClient) Process(ctx context.Context, command goredis.Cmder) error {
	if !command.NoRetry() {
		panic("history append permits retries")
	}
	if err := w.pipeline.Process(ctx, command); err != nil {
		return err
	}
	return command.Err()
}

type writePipeline struct {
	goredis.Pipeliner
	appendErr error
	execErr   error
}

func (w *writePipeline) Process(_ context.Context, command goredis.Cmder) error {
	if !command.NoRetry() {
		panic("queued history append permits retries")
	}
	command.SetErr(w.appendErr)
	return nil
}
func (w *writePipeline) PExpire(ctx context.Context, _ string, _ time.Duration) *goredis.BoolCmd {
	return goredis.NewBoolCmd(ctx)
}
func (w *writePipeline) Exec(context.Context) ([]goredis.Cmder, error) { return nil, w.execErr }

func TestWriteOutcomePreservesAcknowledgedAppendWhenExpiryFails(t *testing.T) {
	cause := errors.New("write transport failed")
	for _, test := range []struct {
		name               string
		ttl                time.Duration
		appendErr, execErr error
		want               history.WriteOutcome
	}{
		{name: "success", want: history.WriteOutcome{Accepted: 2}},
		{name: "unconfirmed append", appendErr: cause, want: history.WriteOutcome{Uncertain: true}},
		{name: "expiry failed after append", ttl: time.Minute, execErr: cause, want: history.WriteOutcome{Accepted: 2}},
		{name: "unconfirmed transaction", ttl: time.Minute, appendErr: cause, execErr: cause, want: history.WriteOutcome{Uncertain: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := writeClient{pipeline: &writePipeline{appendErr: test.appendErr, execErr: test.execErr}}
			store, err := redis.NewStore(t.Context(), redis.StoreConfig{Client: client, TTL: test.ttl})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := store.Write(t.Context(), "conversation", chat.NewUserMessage(chat.NewTextPart("first")), chat.NewAssistantMessage(chat.NewTextPart("second")))
			if outcome != test.want || (test.appendErr != nil || test.execErr != nil) != errors.Is(err, cause) {
				t.Fatalf("outcome=%+v error=%v", outcome, err)
			}
			if validationErr := outcome.ValidateFor(2, err); validationErr != nil {
				t.Fatal(validationErr)
			}
		})
	}
}
