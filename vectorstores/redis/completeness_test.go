package redis

import (
	"errors"
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"
)

func TestSearchCompletenessRejectsUnreadableHit(t *testing.T) {
	result := goredis.FTSearchResult{Docs: []goredis.Document{
		{ID: "a"},
		{ID: "b", Error: errors.New("document is gone")},
	}}
	err := checkSearchCompleteness("documents", result)
	if err == nil || !strings.Contains(err.Error(), `hit "b": document is gone`) {
		t.Fatalf("checkSearchCompleteness() = %v, want a hit error", err)
	}
}
