package redis

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

// DefaultKeyPrefix namespaces history keys so a shared Redis instance does not
// collide with other applications. Callers that run several environments
// against one instance should override it rather than rely on this value.
const DefaultKeyPrefix = "chat:history:"

const historyAppendCommand = "rpush"

var scanPatternEscaper = strings.NewReplacer(
	`\`, `\\`,
	`*`, `\*`,
	`?`, `\?`,
	`[`, `\[`,
	`]`, `\]`,
)

type RedisClient interface {
	Process(context.Context, goredis.Cmder) error
	TxPipeline() goredis.Pipeliner
	LRange(context.Context, string, int64, int64) *goredis.StringSliceCmd
	Del(context.Context, ...string) *goredis.IntCmd
}

type StoreConfig struct {
	Client RedisClient

	// ScanNodes must visit every authoritative node in Client's keyspace and
	// return an error if any node cannot be visited. ClusterClient.ForEachMaster
	// satisfies this contract, including when exposed by a decorator. Ring's
	// ForEachShard does not: unavailable shards can be silently skipped.
	// The host must bind Client and ScanNodes to the same logical Redis store.
	ScanNodes func(context.Context, func(context.Context, *goredis.Client) error) error

	KeyPrefix string

	TTL time.Duration
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("redis: client is required")
	}
	if s.ScanNodes == nil {
		return errors.New("redis: complete keyspace node enumeration is required")
	}
	if s.TTL < 0 {
		return errors.New("redis: TTL must not be negative")
	}
	return nil
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store persists each conversation as an ordered Redis list through a
// caller-owned client. It never closes the client; when TTL is configured,
// append and expiry refresh share one transaction so retention cannot lag a
// successful write.
type Store struct {
	client    RedisClient
	scanNodes func(context.Context, func(context.Context, *goredis.Client) error) error
	keyPrefix string
	ttl       time.Duration
}

// A Redis list needs no provisioned schema, so construction performs no I/O.
func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.KeyPrefix == "" {
		config.KeyPrefix = DefaultKeyPrefix
	}
	return &Store{
		client:    config.Client,
		scanNodes: config.ScanNodes,
		keyPrefix: config.KeyPrefix,
		ttl:       config.TTL,
	}, nil
}

func (s *Store) key(conversationID history.ConversationID) string {
	return s.keyPrefix + conversationID.String()
}

func (s *Store) Write(ctx context.Context, conversationID history.ConversationID, messages ...chat.Message) (outcome history.WriteOutcome, err error) {
	if err = ctx.Err(); err != nil {
		return outcome, err
	}
	if err = conversationID.Validate(); err != nil {
		return outcome, err
	}
	if len(messages) == 0 {
		return history.WriteOutcome{Accepted: len(messages)}, nil
	}

	encoded, err := encodeMessages(messages)
	if err != nil {
		return outcome, fmt.Errorf("redis: write: encode messages: %w", err)
	}
	arguments := make([]any, 2, len(encoded)+2)
	arguments[0], arguments[1] = historyAppendCommand, s.key(conversationID)
	for _, raw := range encoded {
		arguments = append(arguments, raw)
	}

	key := s.key(conversationID)
	appendCommand := &historyAppend{IntCmd: goredis.NewIntCmd(ctx, arguments...)}
	outcome.Uncertain = true
	if s.ttl == 0 {
		if err = s.client.Process(ctx, appendCommand); err != nil {
			return outcome, fmt.Errorf("redis: write: append messages: %w", err)
		}
		return history.WriteOutcome{Accepted: len(messages)}, nil
	}
	transaction := s.client.TxPipeline()
	if err = transaction.Process(ctx, appendCommand); err != nil {
		return history.WriteOutcome{}, fmt.Errorf("redis: queue history append: %w", err)
	}
	transaction.PExpire(ctx, key, s.ttl)
	if _, err = transaction.Exec(ctx); err != nil {
		if appendCommand.Err() == nil {
			outcome = history.WriteOutcome{Accepted: len(messages)}
		}
		return outcome, fmt.Errorf("redis: write: append messages and refresh expiry: %w", err)
	}
	return history.WriteOutcome{Accepted: len(messages)}, nil
}

func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) (storedMessages []chat.Message, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = conversationID.Validate(); err != nil {
		return nil, err
	}

	var encodedMessages []string
	encodedMessages, err = s.client.LRange(ctx, s.key(conversationID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: read: fetch messages: %w", err)
	}

	storedMessages = make([]chat.Message, 0, len(encodedMessages))
	for index, raw := range encodedMessages {
		message, err := decodeMessage([]byte(raw))
		if err != nil {
			return nil, fmt.Errorf("redis: read: decode message %d: %w", index, err)
		}
		storedMessages = append(storedMessages, message)
	}
	return storedMessages, nil
}

func (s *Store) Clear(ctx context.Context, conversationID history.ConversationID) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = conversationID.Validate(); err != nil {
		return err
	}

	if err = s.client.Del(ctx, s.key(conversationID)).Err(); err != nil {
		return fmt.Errorf("redis: clear: delete conversation: %w", err)
	}
	return nil
}

type keyScanner interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *goredis.ScanCmd
}

// SCAN may observe concurrent mutations and repeat keys, so complete node
// coverage does not imply a point-in-time snapshot. Results are de-duplicated.
func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	collector := &conversationCollector{
		keyPrefix: s.keyPrefix,
		match:     scanPatternEscaper.Replace(s.keyPrefix) + "*",
		ids:       make(map[history.ConversationID]struct{}),
	}

	if err := s.scanNodes(ctx, collector.scanNode); err != nil {
		return nil, fmt.Errorf("redis: list conversations: scan keyspace: %w", err)
	}
	return collector.sorted(), nil
}

type conversationCollector struct {
	keyPrefix string
	match     string

	mu  sync.Mutex
	ids map[history.ConversationID]struct{}
}

func (c *conversationCollector) scanNode(ctx context.Context, node *goredis.Client) error {
	if node == nil {
		return errors.New("redis: keyspace enumeration supplied a nil node")
	}
	return c.scan(ctx, node)
}

// scan drives one node's cursor to completion. A cursor is only meaningful to
// the node that issued it, so it never leaves this loop.
func (c *conversationCollector) scan(ctx context.Context, scanner keyScanner) error {
	var cursor uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		keys, next, err := scanner.Scan(ctx, cursor, c.match, 0).Result()
		if err != nil {
			return fmt.Errorf("redis: list conversations: scan keys: %w", err)
		}
		if err := c.collect(keys); err != nil {
			return err
		}

		if next == 0 {
			return nil
		}
		cursor = next
	}
}

func (c *conversationCollector) collect(keys []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, key := range keys {
		id, ok := strings.CutPrefix(key, c.keyPrefix)
		if !ok {
			// MATCH should preclude this, but guard against the
			// prefix incidentally matching unintended keys.
			continue
		}
		conversationID := history.ConversationID(id)
		if err := conversationID.Validate(); err != nil {
			return fmt.Errorf("redis: list conversations: invalid stored ID %q: %w", id, err)
		}
		c.ids[conversationID] = struct{}{}
	}
	return nil
}

func (c *conversationCollector) sorted() []history.ConversationID {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Non-nil even when no conversations exist — every backend's
	// Conversations returns an empty slice, not nil.
	ids := make([]history.ConversationID, 0, len(c.ids))
	for id := range c.ids {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// A history append may already have taken effect when its reply is lost.
// Its command owns the execution permission before entering the SDK.
type historyAppend struct{ *goredis.IntCmd }

func (*historyAppend) NoRetry() bool { return true }
