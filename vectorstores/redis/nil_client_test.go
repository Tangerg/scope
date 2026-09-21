package redis

import (
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"
)

type nilClient struct{ goredis.UniversalClient }

func TestConfigRejectsTypedNilClient(t *testing.T) {
	var client *nilClient
	config := StoreConfig{Client: client}
	err := config.Validate()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "client") {
		t.Fatalf("typed nil client error = %v", err)
	}
}
