package redis

import (
	"context"
	_ "embed"
	jsonv2 "encoding/json/v2"
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

//go:embed history.lua
var historyScript string

const (
	operationAppend        = "append"
	operationRead          = "read"
	operationClear         = "clear"
	operationVisible       = "visible"
	scriptAccepted   int64 = 1
	scriptAbsent     int64 = 0
	scriptConflict   int64 = -1
	scriptClosed     int64 = -2
)

var scanPatternEscaper = strings.NewReplacer(
	`\`, `\\`,
	`*`, `\*`,
	`?`, `\?`,
	`[`, `\[`,
	`]`, `\]`,
)

type RedisClient interface {
	Eval(context.Context, string, []string, ...any) *goredis.Cmd
}

type StoreConfig struct {
	Client RedisClient

	// ScanNodes must visit every authoritative node in Client's keyspace and
	// return an error if any node cannot be visited. ClusterClient.ForEachMaster
	// satisfies this contract, including when exposed by a decorator. Ring's
	// ForEachShard does not: unavailable shards can be silently skipped.
	// The host must bind Client and ScanNodes to the same logical Redis store.
	ScanNodes func(context.Context, func(context.Context, *goredis.Client) error) error

	// Braces are reserved for the conversation hash tag that keeps script keys
	// in one Redis Cluster slot.
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
	return (conversationKeyspace{prefix: s.KeyPrefix}).validate()
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store persists each conversation as an ordered Redis list through a
// caller-owned client. It never closes the client; when TTL is configured,
// append, durable receipt and message expiry share one script. Receipts and
// closed conversation markers never inherit the message TTL.
type Store struct {
	client    RedisClient
	scanNodes func(context.Context, func(context.Context, *goredis.Client) error) error
	keys      conversationKeyspace
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
		keys:      conversationKeyspace{prefix: config.KeyPrefix},
		ttl:       config.TTL,
	}, nil
}

func (s *Store) scriptKeys(id history.ConversationID) []string {
	return []string{s.keys.messages(id), s.keys.ledger(id)}
}

func (s *Store) Append(ctx context.Context, batch history.Batch) (history.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return history.Receipt{}, err
	}
	if err := batch.Validate(); err != nil {
		return history.Receipt{}, err
	}
	ttl := int64(0)
	if s.ttl > 0 {
		ttl = max(1, s.ttl.Milliseconds())
	}
	status, err := s.client.Eval(ctx, historyScript, s.scriptKeys(batch.ConversationID()), operationAppend, batch.ID().String(), batch.Digest(), string(batch.EncodedMessages()), ttl).Int64()
	if err != nil {
		return history.Receipt{}, fmt.Errorf("redis: append batch: %w", err)
	}
	switch status {
	case scriptConflict:
		return history.Receipt{}, history.ErrAppendConflict
	case scriptClosed:
		return history.Receipt{}, history.ErrConversationClosed
	case scriptAccepted:
		return history.Receipt{ConversationID: batch.ConversationID(), ID: batch.ID(), Digest: batch.Digest()}, nil
	default:
		return history.Receipt{}, fmt.Errorf("redis: unexpected append acknowledgment %d", status)
	}
}

func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := conversationID.Validate(); err != nil {
		return nil, err
	}
	encoded, err := s.client.Eval(ctx, historyScript, s.scriptKeys(conversationID), operationRead).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("redis: read batches: %w", err)
	}
	messages := []chat.Message{}
	for index, raw := range encoded {
		var batch []chat.Message
		if err := jsonv2.Unmarshal([]byte(raw), &batch); err != nil {
			return nil, fmt.Errorf("redis: decode batch %d: %w", index, err)
		}
		if len(batch) == 0 {
			return nil, errors.New("redis: stored batch is empty")
		}
		messages = append(messages, batch...)
	}
	return messages, nil
}

func (s *Store) Clear(ctx context.Context, conversationID history.ConversationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conversationID.Validate(); err != nil {
		return err
	}
	status, err := s.client.Eval(ctx, historyScript, s.scriptKeys(conversationID), operationClear).Int64()
	if err != nil {
		return fmt.Errorf("redis: close conversation: %w", err)
	}
	if status != scriptAccepted {
		return fmt.Errorf("redis: unexpected clear acknowledgment %d", status)
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
		keys:  s.keys,
		match: s.keys.pattern(),
		ids:   make(map[history.ConversationID]struct{}),
	}

	if err := s.scanNodes(ctx, collector.scanNode); err != nil {
		return nil, fmt.Errorf("redis: list conversations: scan keyspace: %w", err)
	}
	candidates := collector.sorted()
	visible := candidates[:0]
	for _, id := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// A failed physical cleanup can leave messages behind after the ledger
		// closes an identity. Route through Client so cluster slot moves retain
		// the same lifecycle authority as Read and Append.
		status, err := s.client.Eval(ctx, historyScript, s.scriptKeys(id), operationVisible).Int64()
		if err != nil {
			return nil, fmt.Errorf("redis: list conversations: check visibility: %w", err)
		}
		switch status {
		case scriptAccepted:
			visible = append(visible, id)
		case scriptAbsent, scriptClosed:
		default:
			return nil, fmt.Errorf("redis: unexpected conversation visibility %d", status)
		}
	}
	return visible, nil
}

type conversationCollector struct {
	keys  conversationKeyspace
	match string

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
		conversationID, ok, err := c.keys.conversation(key)
		if err != nil {
			return err
		}
		if !ok {
			continue
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
