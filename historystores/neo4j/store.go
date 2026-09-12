package neo4j

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultDatabase = "neo4j"
	DefaultLabel    = "ChatMessage"

	fieldConversationID = "conversation_id"
	fieldMessage        = "message"
	fieldSequence       = "seq"
	parameterRows       = "rows"
)

// StoreConfig names every dependency explicitly rather than defaulting a
// client or connection, so a store cannot be built against a service the
// caller did not choose.
type StoreConfig struct {
	// Driver is the live Neo4j driver. Required. Callers own its
	// lifetime.
	Driver neo4j.DriverWithContext

	// Database selects the Neo4j database to operate against.
	// Optional: defaults to [DefaultDatabase] ("neo4j").
	Database string

	// Label is the node label used for stored messages. Optional:
	// defaults to [DefaultLabel] ("ChatMessage").
	Label string

	// InitializeSchema, when true, creates an index on
	// (conversation_id, seq) for the chosen label. Idempotent.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Driver) {
		return errors.New("neo4j: driver is required")
	}
	if s.Label != "" && !validIdentifier(s.Label) {
		return fmt.Errorf("neo4j: label %q must be a valid unquoted identifier", s.Label)
	}
	return nil
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store persists messages as Neo4j nodes through a caller-owned driver. It
// validates dynamic labels before query construction and uses a local monotonic
// sequence for deterministic reads; ordering across separate Store values
// remains unspecified.
type Store struct {
	driver   neo4j.DriverWithContext
	database string
	label    string
	sequence *history.Sequence
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its graph schema and index exist would
// fail on the first index rather than at wiring, where the misconfiguration
// actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Database == "" {
		config.Database = DefaultDatabase
	}
	if config.Label == "" {
		config.Label = DefaultLabel
	}
	sequence, err := history.NewSequence(time.Nanosecond)
	if err != nil {
		return nil, err
	}
	s := &Store{
		driver:   config.Driver,
		database: config.Database,
		label:    config.Label,
		sequence: sequence,
	}
	if config.InitializeSchema {
		if err := s.initIndex(ctx); err != nil {
			return nil, fmt.Errorf("neo4j: initialize schema: %w", err)
		}
	}
	return s, nil
}

// initIndex creates the (conversation_id, seq) range index on the
// configured label. Idempotent.
func (s *Store) initIndex(ctx context.Context) error {
	indexName := s.label + "_conversation_seq_idx"
	cypher := fmt.Sprintf(
		"CREATE INDEX %s IF NOT EXISTS FOR (m:%s) ON (m.conversation_id, m.seq)",
		indexName, s.label,
	)
	_, err := neo4j.ExecuteQuery(ctx, s.driver, cypher, nil,
		neo4j.EagerResultTransformer,
		neo4j.ExecuteQueryWithDatabase(s.database),
	)
	return err
}

// Write creates a new node per message under conversationID. A reserved
// sequence range preserves argument order and remains monotonic if the local
// clock moves backward.
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
		return outcome, fmt.Errorf("neo4j: write: encode messages: %w", err)
	}
	sequenceBase := s.sequence.Reserve(len(encoded))
	rows := make([]map[string]any, 0, len(encoded))
	for index, raw := range encoded {
		rows = append(rows, map[string]any{
			fieldConversationID: conversationID.String(),
			fieldSequence:       sequenceBase + int64(index),
			fieldMessage:        string(raw),
		})
	}

	cypher := fmt.Sprintf(`
		UNWIND $rows AS row
		CREATE (m:%s {
			conversation_id: row.conversation_id,
			seq:             row.seq,
			message:         row.message,
			created_at:      datetime()
		})`, s.label)

	outcome.Uncertain = true
	_, err = neo4j.ExecuteQuery(ctx, s.driver, cypher,
		map[string]any{parameterRows: rows},
		neo4j.EagerResultTransformer,
		neo4j.ExecuteQueryWithDatabase(s.database),
	)
	if err != nil {
		return outcome, fmt.Errorf("neo4j: write: create message nodes: %w", err)
	}
	return history.WriteOutcome{Accepted: len(messages)}, nil
}

// Read returns every message stored under conversationID in
// insertion order (seq ascending).
func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) (storedMessages []chat.Message, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = conversationID.Validate(); err != nil {
		return nil, err
	}

	cypher := fmt.Sprintf(
		"MATCH (m:%s {conversation_id: $conversation_id}) RETURN elementId(m) AS id, m.seq AS seq, m.message AS message ORDER BY seq ASC, id ASC",
		s.label,
	)
	var result *neo4j.EagerResult
	result, err = neo4j.ExecuteQuery(ctx, s.driver, cypher,
		map[string]any{fieldConversationID: conversationID.String()},
		neo4j.EagerResultTransformer,
		neo4j.ExecuteQueryWithDatabase(s.database),
	)
	if err != nil {
		return nil, fmt.Errorf("neo4j: read: query messages: %w", err)
	}

	storedMessages = make([]chat.Message, 0, len(result.Records))
	for index, record := range result.Records {
		rawSequence, ok := record.Get(fieldSequence)
		if !ok {
			return nil, fmt.Errorf("neo4j: read: record %d is missing sequence", index)
		}
		sequence, ok := rawSequence.(int64)
		if !ok || sequence <= 0 {
			return nil, fmt.Errorf("neo4j: read: record %d sequence is %v (%T), want a positive integer", index, rawSequence, rawSequence)
		}
		raw, ok := record.Get(fieldMessage)
		if !ok {
			return nil, fmt.Errorf("neo4j: read: record %d is missing message", index)
		}
		encodedMessage, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("neo4j: read: record %d message has type %T, want string", index, raw)
		}
		message, err := decodeMessage([]byte(encodedMessage))
		if err != nil {
			return nil, fmt.Errorf("neo4j: read: decode message %d: %w", index, err)
		}
		storedMessages = append(storedMessages, message)
	}
	return storedMessages, nil
}

// Clear deletes every node for conversationID under the configured
// label. Unknown ids are a no-op.
func (s *Store) Clear(ctx context.Context, conversationID history.ConversationID) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = conversationID.Validate(); err != nil {
		return err
	}

	cypher := fmt.Sprintf(
		"MATCH (m:%s {conversation_id: $conversation_id}) DELETE m",
		s.label,
	)
	_, err = neo4j.ExecuteQuery(ctx, s.driver, cypher,
		map[string]any{fieldConversationID: conversationID.String()},
		neo4j.EagerResultTransformer,
		neo4j.ExecuteQueryWithDatabase(s.database),
	)
	if err != nil {
		return fmt.Errorf("neo4j: clear: delete message nodes: %w", err)
	}
	return nil
}

// Conversations returns every stored conversation ID in lexical order.
func (s *Store) Conversations(ctx context.Context) (ids []history.ConversationID, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	cypher := fmt.Sprintf(
		"MATCH (m:%s) RETURN DISTINCT m.conversation_id AS id",
		s.label,
	)
	var result *neo4j.EagerResult
	result, err = neo4j.ExecuteQuery(ctx, s.driver, cypher, nil,
		neo4j.EagerResultTransformer,
		neo4j.ExecuteQueryWithDatabase(s.database),
	)
	if err != nil {
		return nil, fmt.Errorf("neo4j: list conversations: query IDs: %w", err)
	}

	ids = make([]history.ConversationID, 0, len(result.Records))
	for index, record := range result.Records {
		raw, ok := record.Get("id")
		if !ok {
			return nil, fmt.Errorf("neo4j: list conversations: record %d is missing ID", index)
		}
		id, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("neo4j: list conversations: record %d ID has type %T, want string", index, raw)
		}
		conversationID := history.ConversationID(id)
		if err := conversationID.Validate(); err != nil {
			return nil, fmt.Errorf("neo4j: list conversations: record %d has invalid ID %q: %w", index, id, err)
		}
		ids = append(ids, conversationID)
	}
	slices.Sort(ids)
	return ids, nil
}
