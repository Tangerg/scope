//go:build integration

package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/redis"
)

func aclStore(t *testing.T, server *redisProcess, denied string, ttl time.Duration) *redis.Store {
	t.Helper()
	const username = "history-fixture"
	const password = "fixture-password"
	if err := server.admin.Do(t.Context(), "ACL", "SETUSER", username, "reset", "on", ">"+password, "~*", "&*", "+@all", "-"+denied).Err(); err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(&goredis.Options{Addr: server.address, Username: username, Password: password, Protocol: 2, DisableIdentity: true, MaxRetries: -1})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	store, err := redis.NewStore(t.Context(), redis.StoreConfig{Client: client, TTL: ttl, ScanNodes: func(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
		return visit(ctx, client)
	}})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAppendACLFailureLeavesTheSameBatchRetrySafe(t *testing.T) {
	for _, command := range []string{"hset", "rpush", "pexpire"} {
		t.Run(command, func(t *testing.T) {
			server := newRedisProcess(t)
			store := aclStore(t, server, command, time.Minute)
			batch := historyBatch(t, "acl-conversation", "same-batch", "exactly once")
			receipt, err := store.Append(t.Context(), batch)
			if err == nil || receipt != (history.Receipt{}) {
				t.Fatalf("rejected append=%+v error=%v", receipt, err)
			}
			if count, err := server.admin.DBSize(t.Context()).Result(); err != nil || count != 0 {
				t.Fatalf("partial append keys=%d error=%v", count, err)
			}
			if err := server.admin.Do(t.Context(), "ACL", "SETUSER", "history-fixture", "+"+command).Err(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				receipt, err = store.Append(t.Context(), batch)
				if err != nil || receipt.ValidateFor(batch) != nil {
					t.Fatalf("same-batch retry=%+v error=%v", receipt, err)
				}
			}
			messages, err := store.Read(t.Context(), batch.ConversationID())
			if err != nil || len(messages) != 1 || messages[0].Text() != "exactly once" {
				t.Fatalf("retried history=%v error=%v", messages, err)
			}
		})
	}
}

func TestClearCleanupFailureKeepsConversationClosed(t *testing.T) {
	server := newRedisProcess(t)
	store := aclStore(t, server, "del", 0)
	batch := historyBatch(t, "closed-conversation", "batch", "private message")
	if _, err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(t.Context(), batch.ConversationID()); err == nil {
		t.Fatal("denied physical removal succeeded")
	}
	if receipt, err := store.Append(t.Context(), batch); receipt != (history.Receipt{}) || !errors.Is(err, history.ErrConversationClosed) {
		t.Fatalf("closed append=%+v error=%v", receipt, err)
	}
	if messages, err := store.Read(t.Context(), batch.ConversationID()); err != nil || len(messages) != 0 {
		t.Fatalf("closed history=%v error=%v", messages, err)
	}
	if ids, err := store.Conversations(t.Context()); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("closed listing=%v error=%v", ids, err)
	}
	if err := server.admin.Do(t.Context(), "ACL", "SETUSER", "history-fixture", "+del").Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(t.Context(), batch.ConversationID()); err != nil {
		t.Fatal(err)
	}
	if count, err := server.admin.DBSize(t.Context()).Result(); err != nil || count != 1 {
		t.Fatalf("closed ledger keys=%d error=%v", count, err)
	}
}
