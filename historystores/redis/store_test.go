package redis_test

import (
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/historystores/redis"
)

func stubClient() goredis.UniversalClient {
	return goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0"})
}

func TestNewStoreRequiresClient(t *testing.T) {
	config := redis.StoreConfig{}
	if err := config.Validate(); err == nil {
		t.Fatal("StoreConfig.Validate should reject a nil Client")
	}
	_, err := redis.NewStore(t.Context(), config)
	if err == nil {
		t.Fatal("expected error when Client is nil")
	}
	if !strings.Contains(err.Error(), "client") {
		t.Fatalf("err = %v; should mention client", err)
	}
	var typedNil *goredis.Client
	if _, err := redis.NewStore(t.Context(), redis.StoreConfig{Client: typedNil}); err == nil {
		t.Fatal("expected error when Client is a typed nil")
	}
}

func TestNewStoreRejectsNegativeTTL(t *testing.T) {
	_, err := redis.NewStore(t.Context(), redis.StoreConfig{
		Client: stubClient(),
		TTL:    -1 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error on negative TTL")
	}
}

func TestNewStoreDefaultsKeyPrefix(t *testing.T) {
	if _, err := redis.NewStore(t.Context(), redis.StoreConfig{Client: stubClient()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
