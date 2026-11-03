package redis

import (
	"context"
	"errors"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider              = "Redis"
	DefaultIndexName      = "scope-vector-index"
	contentField          = "content"
	embeddingField        = "embedding"
	metadataField         = "metadata_json"
	termsField            = "metadata_terms"
	DefaultWriteBatchSize = 256
	distanceFieldName     = "__vector_distance"
	vectorParamName       = "scope_query_vec"
)

type hashReader interface {
	Eval(context.Context, string, []string, ...any) *goredis.Cmd
}

// RedisClient supplies host-owned native commands. StoreConfig.ScanNodes owns
// complete keyspace traversal independently of command-client decorators.
type RedisClient interface {
	hashReader
	Do(context.Context, ...any) *goredis.Cmd
	Pipeline() goredis.Pipeliner
}

type StoreConfig struct {
	Client    RedisClient
	IndexName string
	Space     vectorstore.SpaceID
	// ScanNodes visits every authoritative node in Client's keyspace and fails
	// if any node is unavailable. ClusterClient.ForEachMaster satisfies this
	// contract; Ring.ForEachShard does not, because it can skip failed shards.
	// The host binds commands and traversal to the same logical Redis store.
	ScanNodes func(context.Context, func(context.Context, *goredis.Client) error) error
	// WriteBatchSize bounds commands submitted in one nontransactional pipeline.
	WriteBatchSize int
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("redis: Client is required")
	}
	if s.ScanNodes == nil {
		return errors.New("redis: complete keyspace node enumeration is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.WriteBatchSize < 0 {
		return errors.New("redis: WriteBatchSize cannot be negative")
	}
	if s.IndexName != "" && strings.TrimSpace(s.IndexName) != s.IndexName {
		return errors.New("redis: IndexName must not have surrounding whitespace")
	}
	return nil
}
