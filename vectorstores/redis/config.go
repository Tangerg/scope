package redis

import (
	"context"
	"errors"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider          = "Redis"
	DefaultIndexName  = "scope-vector-index"
	contentField      = "content"
	embeddingField    = "embedding"
	metadataField     = "metadata_json"
	distanceFieldName = "__vector_distance"
	vectorParamName   = "scope_query_vec"
)

type hashScanner interface {
	Scan(context.Context, uint64, string, int64) *goredis.ScanCmd
	HGetAll(context.Context, string) *goredis.MapStringStringCmd
}

// RedisClient owns native commands, topology, credentials, retries and lifetime.
// Scan must enumerate the complete namespace. Store uses ForEachMaster for the
// native ClusterClient; Ring cannot provide this guarantee and is rejected.
type RedisClient interface {
	hashScanner
	Do(context.Context, ...any) *goredis.Cmd
	Eval(context.Context, string, []string, ...any) *goredis.Cmd
	Pipeline() goredis.Pipeliner
}

type StoreConfig struct {
	Client          RedisClient
	IndexName       string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("redis: Client is required")
	}
	if _, ring := s.Client.(*goredis.Ring); ring {
		return errors.ErrUnsupported
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("redis: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("redis: DocumentBatcher is required")
	}
	if s.IndexName != "" && strings.TrimSpace(s.IndexName) != s.IndexName {
		return errors.New("redis: IndexName must not have surrounding whitespace")
	}
	return nil
}
