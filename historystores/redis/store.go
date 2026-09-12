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

var scanPatternEscaper = strings.NewReplacer(
	`\`, `\\`,
	`*`, `\*`,
	`?`, `\?`,
	`[`, `\[`,
	`]`, `\]`,
)

// StoreConfig names every dependency explicitly rather than defaulting a
// client or connection, so a store cannot be built against a service the
// caller did not choose.
type StoreConfig struct {
	// Client is the live go-redis client. Required. The store does
	// not take ownership — callers Close() the client themselves.
	Client goredis.UniversalClient

	// KeyPrefix is prepended to every conversation id to namespace the
	// keys. Optional: defaults to [DefaultKeyPrefix].
	KeyPrefix string

	// TTL, when non-zero, applies a millisecond-precision expiry to every
	// conversation key and refreshes it on each Write. Zero means "never
	// expire".
	TTL time.Duration
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("redis: client is required")
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
	client    goredis.UniversalClient
	keyPrefix string
	ttl       time.Duration
}

// NewStore performs no I/O. A Redis list needs no schema, and the only thing
// this store assumes about the server — that its keys are namespaced under
// KeyPrefix — is a fact about the store's own writes rather than something to
// confirm.
//
// The context is still taken, because every store in this family is
// constructed the same way and a caller should not have to remember which
// backend happens to be checkable.
func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.KeyPrefix == "" {
		config.KeyPrefix = DefaultKeyPrefix
	}
	return &Store{
		client:    config.Client,
		keyPrefix: config.KeyPrefix,
		ttl:       config.TTL,
	}, nil
}

// key returns the namespaced Redis key for a conversation id.
func (s *Store) key(conversationID history.ConversationID) string {
	return s.keyPrefix + conversationID.String()
}

// Write appends every message under conversationID. When TTL is set, append
// and expiry refresh execute in one Redis transaction. Empty writes are a
// no-op.
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
	payloads := make([]any, len(encoded))
	for index, raw := range encoded {
		payloads[index] = raw
	}

	key := s.key(conversationID)
	outcome.Uncertain = true
	if s.ttl == 0 {
		if err = s.client.RPush(ctx, key, payloads...).Err(); err != nil {
			return outcome, fmt.Errorf("redis: write: append messages: %w", err)
		}
		return history.WriteOutcome{Accepted: len(messages)}, nil
	}
	transaction := s.client.TxPipeline()
	appendCommand := transaction.RPush(ctx, key, payloads...)
	transaction.PExpire(ctx, key, s.ttl)
	if _, err = transaction.Exec(ctx); err != nil {
		if appendCommand.Err() == nil {
			outcome = history.WriteOutcome{Accepted: len(messages)}
		}
		return outcome, fmt.Errorf("redis: write: append messages and refresh expiry: %w", err)
	}
	return history.WriteOutcome{Accepted: len(messages)}, nil
}

// Read returns every message stored under conversationID in
// insertion order. An empty slice is returned for unknown ids.
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

// Clear drops the entire list for conversationID. Unknown ids are
// silently ignored (DEL on a missing key is a no-op in Redis).
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

// keyScanner is the single Redis operation conversation enumeration needs. It
// is declared here so the scan can run against the whole client or against one
// node of a multi-node client, which is what makes the enumeration complete.
type keyScanner interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *goredis.ScanCmd
}

// Conversations enumerates stored conversation IDs via SCAN and returns them
// in lexical order. SCAN may observe concurrent mutations and repeat keys, so
// results are de-duplicated.
//
// A cluster or ring splits the keyspace across nodes while SCAN carries no key,
// so go-redis routes each call through its shard picker — a round robin by
// default. A single loop therefore asked a different node each iteration and
// fed it a cursor belonging to the previous one, and cursors are per-node: the
// result was an arbitrary subset that changed between calls, reported as
// success. Lister tolerates a concurrent write appearing or not, not a settled
// conversation going missing, so each node is scanned to its own completion.
func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	collector := &conversationCollector{
		keyPrefix: s.keyPrefix,
		match:     scanPatternEscaper.Replace(s.keyPrefix) + "*",
		seen:      make(map[string]struct{}),
	}

	// ForEachMaster and ForEachShard run concurrently and return the first
	// error, which is why the collector is guarded. ForEachShard skips a shard
	// it considers down, so a ring missing a shard enumerates the rest — those
	// conversations are unreachable through Read and Write as well.
	//
	// Only go-redis's own multi-node types can be recognized. A caller who
	// hands over some wrapper that hides a cluster behind UniversalClient gets
	// the single-client scan, because nothing here can unwrap it; refusing
	// every unrecognized implementation instead would reject the ordinary case,
	// a tracing decorator over a plain client.
	switch client := s.client.(type) {
	case *goredis.ClusterClient:
		if err := client.ForEachMaster(ctx, collector.scanNode); err != nil {
			return nil, fmt.Errorf("redis: list conversations: scan cluster masters: %w", err)
		}
	case *goredis.Ring:
		if err := client.ForEachShard(ctx, collector.scanNode); err != nil {
			return nil, fmt.Errorf("redis: list conversations: scan ring shards: %w", err)
		}
	default:
		if err := collector.scan(ctx, s.client); err != nil {
			return nil, err
		}
	}
	return collector.sorted(), nil
}

// conversationCollector accumulates conversation ids across one or more SCAN
// cursors.
type conversationCollector struct {
	keyPrefix string
	match     string

	mu   sync.Mutex
	seen map[string]struct{}
	ids  []history.ConversationID
}

func (c *conversationCollector) scanNode(ctx context.Context, node *goredis.Client) error {
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
		c.collect(keys)

		if next == 0 {
			return nil
		}
		cursor = next
	}
}

func (c *conversationCollector) collect(keys []string) {
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
		if conversationID.Validate() != nil {
			continue
		}
		if _, duplicate := c.seen[id]; duplicate {
			continue
		}
		c.seen[id] = struct{}{}
		c.ids = append(c.ids, conversationID)
	}
}

func (c *conversationCollector) sorted() []history.ConversationID {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Non-nil even when no conversations exist — every backend's
	// Conversations returns an empty slice, not nil.
	ids := make([]history.ConversationID, len(c.ids))
	copy(ids, c.ids)
	slices.Sort(ids)
	return ids
}
