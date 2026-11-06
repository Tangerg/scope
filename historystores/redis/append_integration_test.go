//go:build integration

package redis_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/storetest"
	"github.com/Tangerg/scope/historystores/redis"
)

func historyBatch(t *testing.T, conversation, id, text string) history.Batch {
	t.Helper()
	batch, err := history.NewBatch(history.ConversationID(conversation), history.AppendID(id), chat.NewUserMessage(chat.NewTextPart(text)))
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func redisStore(t *testing.T, server *redisProcess, ttl time.Duration, dialer func(context.Context, string, string) (net.Conn, error)) (*redis.Store, *goredis.Client) {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: server.address, Protocol: 2, DisableIdentity: true, Dialer: dialer})
	t.Cleanup(func() { _ = client.Close() })
	store, err := redis.NewStore(t.Context(), redis.StoreConfig{Client: client, TTL: ttl, ScanNodes: func(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
		return visit(ctx, client)
	}})
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

func TestAtomicContract(t *testing.T) {
	store, _ := redisStore(t, newRedisProcess(t), 0, nil)
	storetest.RunAtomic(t, store)
}

func TestAppendIdentitySurvivesConcurrencyAndMessageExpiry(t *testing.T) {
	server := newRedisProcess(t)
	store, _ := redisStore(t, server, time.Minute, nil)
	other, _ := redisStore(t, server, time.Minute, nil)
	batch := historyBatch(t, "user:{shared}/会话", "first", "once")
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			receipt, err := other.Append(t.Context(), batch)
			if err != nil || receipt.ValidateFor(batch) != nil {
				t.Errorf("Append = %+v, %v", receipt, err)
			}
		})
	}
	workers.Wait()
	if messages, err := store.Read(t.Context(), batch.ConversationID()); err != nil || len(messages) != 1 {
		t.Fatalf("Read = %v, %v", messages, err)
	}
	conflict := historyBatch(t, batch.ConversationID().String(), batch.ID().String(), "changed")
	if receipt, err := store.Append(t.Context(), conflict); receipt != (history.Receipt{}) || !errors.Is(err, history.ErrAppendConflict) {
		t.Fatalf("conflict = %+v, %v", receipt, err)
	}
	server.expireMessages(t)
	if receipt, err := other.Append(t.Context(), batch); err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("expired replay = %+v, %v", receipt, err)
	}
	if messages, err := store.Read(t.Context(), batch.ConversationID()); err != nil || messages == nil || len(messages) != 0 {
		t.Fatalf("old batch resurrected = %v, %v", messages, err)
	}
	if ids, err := store.Conversations(t.Context()); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("expired conversations = %v, %v", ids, err)
	}
}

func TestClearPermanentlyClosesEvenUnknownConversation(t *testing.T) {
	server := newRedisProcess(t)
	store, _ := redisStore(t, server, 0, nil)
	for _, id := range []string{"existing", "unknown"} {
		batch := historyBatch(t, id, "attempt", "message")
		if id == "existing" {
			if _, err := store.Append(t.Context(), batch); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Clear(t.Context(), batch.ConversationID()); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if receipt, err := store.Append(t.Context(), batch); receipt != (history.Receipt{}) || !errors.Is(err, history.ErrConversationClosed) {
				t.Fatalf("closed append = %+v, %v", receipt, err)
			}
			if err := store.Clear(t.Context(), batch.ConversationID()); err != nil {
				t.Fatal(err)
			}
		}
		if messages, err := store.Read(t.Context(), batch.ConversationID()); err != nil || messages == nil || len(messages) != 0 {
			t.Fatalf("closed Read = %v, %v", messages, err)
		}
	}
	if ids, err := store.Conversations(t.Context()); err != nil || len(ids) != 0 {
		t.Fatalf("closed conversations = %v, %v", ids, err)
	}
}

type droppedReplyDialer struct {
	dropped  atomic.Bool
	attempts atomic.Int32
}

func (d *droppedReplyDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &droppedReplyConnection{Conn: connection, owner: d}, nil
}

type droppedReplyConnection struct {
	net.Conn
	owner *droppedReplyDialer
	drop  bool
}

func (d *droppedReplyConnection) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("\r\n$4\r\neval\r\n")) {
		d.owner.attempts.Add(1)
		d.drop = d.owner.dropped.CompareAndSwap(false, true)
	}
	return d.Conn.Write(data)
}

func (d *droppedReplyConnection) Read(data []byte) (int, error) {
	n, err := d.Conn.Read(data)
	if d.drop && n > 0 {
		_ = d.Close()
		return 0, io.EOF
	}
	return n, err
}

func TestSDKRetryAfterLostReplyAppendsExactlyOnce(t *testing.T) {
	server := newRedisProcess(t)
	dialer := &droppedReplyDialer{}
	store, _ := redisStore(t, server, time.Minute, dialer.dial)
	batch := historyBatch(t, "lost reply", "stable", "once")
	receipt, err := store.Append(t.Context(), batch)
	if err != nil || receipt.ValidateFor(batch) != nil || dialer.attempts.Load() < 2 {
		t.Fatalf("Append = %+v, %v; attempts=%d", receipt, err, dialer.attempts.Load())
	}
	if messages, err := store.Read(t.Context(), batch.ConversationID()); err != nil || len(messages) != 1 || messages[0].Text() != "once" {
		t.Fatalf("Read = %v, %v", messages, err)
	}
}
