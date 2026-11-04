package redis_test

import (
	"context"
	"slices"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/historystores/redis"
)

type keyRecordingClient struct{ keys []string }

func (k *keyRecordingClient) Eval(_ context.Context, _ string, keys []string, _ ...any) *goredis.Cmd {
	k.keys = slices.Clone(keys)
	return goredis.NewCmdResult([]any{}, nil)
}

func TestKeyPrefixCannotOverrideConversationHashTag(t *testing.T) {
	for _, prefix := range []string{"{}:", "{tenant}:", "unclosed{", "closing}"} {
		t.Run(prefix, func(t *testing.T) {
			client := &keyRecordingClient{}
			config := redis.StoreConfig{Client: client, ScanNodes: noEnumeration, KeyPrefix: prefix}
			if err := config.Validate(); err == nil {
				t.Fatal("prefix may override the conversation hash tag")
			}
			if store, err := redis.NewStore(t.Context(), config); err == nil || store != nil || client.keys != nil {
				t.Fatalf("invalid prefix reached a usable store: %v, %v, %v", store, err, client.keys)
			}
		})
	}
}

func TestScriptKeysShareTheConversationHashTag(t *testing.T) {
	for _, prefix := range []string{"", "tenant:*?[name]\\:"} {
		t.Run(prefix, func(t *testing.T) {
			client := &keyRecordingClient{}
			store, err := redis.NewStore(t.Context(), redis.StoreConfig{Client: client, ScanNodes: noEnumeration, KeyPrefix: prefix})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read(t.Context(), "conversation"); err != nil {
				t.Fatal(err)
			}
			if prefix == "" {
				prefix = redis.DefaultKeyPrefix
			}
			want := []string{prefix + "{Y29udmVyc2F0aW9u}:messages", prefix + "{Y29udmVyc2F0aW9u}:receipts"}
			if !slices.Equal(client.keys, want) {
				t.Fatalf("script keys = %q, want %q", client.keys, want)
			}
		})
	}
}
