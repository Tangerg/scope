package neo4j

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const (
	DefaultDatabase         = "neo4j"
	DefaultAppendLabel      = "ChatHistory"
	conversationLabelSuffix = "Conversation"
	fieldConversationID     = "conversation_id"
	fieldAppendID           = "append_id"
	fieldDigest             = "digest"
	fieldMessages           = "messages"
	fieldSequence           = "seq"
	fieldClosed             = "closed"
)

type StoreConfig struct {
	Driver            neo4j.Driver
	Database          string
	AppendLabel       string
	ConversationLabel string
	InitializeSchema  bool
}

func (s StoreConfig) resolved() StoreConfig {
	s.Database = cmp.Or(s.Database, DefaultDatabase)
	s.AppendLabel = cmp.Or(s.AppendLabel, DefaultAppendLabel)
	s.ConversationLabel = cmp.Or(s.ConversationLabel, s.AppendLabel+conversationLabelSuffix)
	return s
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Driver) {
		return errors.New("neo4j: driver is required")
	}
	effective := s.resolved()
	if !validIdentifier(effective.AppendLabel) || !validIdentifier(effective.ConversationLabel) {
		return errors.New("neo4j: labels must be valid unquoted identifiers")
	}
	if effective.AppendLabel == effective.ConversationLabel {
		return errors.New("neo4j: append and conversation labels must be distinct")
	}
	return nil
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Uniqueness constraints establish one durable conversation gate. Each query
// locks that gate before reading closure or receipts, so separate drivers cannot
// create competing append decisions under Neo4j's read-committed isolation.
type Store struct {
	driver            neo4j.Driver
	database          string
	appendLabel       string
	conversationLabel string
	appendQuery       string
	readQuery         string
	clearQuery        string
	cleanupQuery      string
	listQuery         string
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.resolved()
	s := &Store{driver: config.Driver, database: config.Database, appendLabel: config.AppendLabel, conversationLabel: config.ConversationLabel}
	// A temporary property acquires the native write lock before any dependent
	// read. Removing it does not release that lock before transaction completion.
	gate := fmt.Sprintf(`MERGE (c:%s {conversation_id: $conversation_id})
		ON CREATE SET c.closed = false, c.last_sequence = 0
		SET c._scope_lock = true REMOVE c._scope_lock
		WITH c`, s.conversationLabel)
	s.appendQuery = gate + fmt.Sprintf(`
		OPTIONAL MATCH (existing:%s {conversation_id: $conversation_id, append_id: $append_id})
		FOREACH (unused IN CASE WHEN NOT c.closed AND existing IS NULL THEN [1] ELSE [] END |
			SET c.last_sequence = c.last_sequence + 1
			CREATE (a:%s {conversation_id: $conversation_id, append_id: $append_id, digest: $digest, messages: $messages, seq: c.last_sequence}))
		WITH c
		OPTIONAL MATCH (a:%s {conversation_id: $conversation_id, append_id: $append_id})
		RETURN c.closed AS closed, a.digest AS digest`, s.appendLabel, s.appendLabel, s.appendLabel)
	s.readQuery = fmt.Sprintf(`MATCH (c:%s {conversation_id: $conversation_id})
		SET c._scope_lock = true REMOVE c._scope_lock
		WITH c WHERE NOT c.closed
		MATCH (a:%s {conversation_id: $conversation_id})
		RETURN a.seq AS seq, a.messages AS messages ORDER BY seq`, s.conversationLabel, s.appendLabel)
	s.clearQuery = gate + ` SET c.closed = true RETURN c.closed AS closed`
	s.cleanupQuery = fmt.Sprintf(`MATCH (c:%s {conversation_id: $conversation_id})
		WHERE c.closed
		OPTIONAL MATCH (a:%s {conversation_id: $conversation_id})
		WITH c, collect(a) AS appends
		FOREACH (a IN appends | REMOVE a.messages)
		RETURN c.closed AS closed`, s.conversationLabel, s.appendLabel)
	s.listQuery = fmt.Sprintf("MATCH (c:%s) WHERE NOT c.closed RETURN c.conversation_id AS conversation_id", s.conversationLabel)
	if config.InitializeSchema {
		for _, query := range []string{
			fmt.Sprintf("CREATE CONSTRAINT %s_id_unique IF NOT EXISTS FOR (c:%s) REQUIRE c.conversation_id IS UNIQUE", s.conversationLabel, s.conversationLabel),
			fmt.Sprintf("CREATE CONSTRAINT %s_id_unique IF NOT EXISTS FOR (a:%s) REQUIRE (a.conversation_id, a.append_id) IS UNIQUE", s.appendLabel, s.appendLabel),
			fmt.Sprintf("CREATE INDEX %s_order IF NOT EXISTS FOR (a:%s) ON (a.conversation_id, a.seq)", s.appendLabel, s.appendLabel),
		} {
			if _, err := s.query(ctx, query, nil); err != nil {
				return nil, fmt.Errorf("neo4j: initialize schema: %w", err)
			}
		}
	}
	if err := s.verifyConstraints(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) query(ctx context.Context, query string, parameters map[string]any) (*neo4j.EagerResult, error) {
	return neo4j.ExecuteQuery(ctx, s.driver, query, parameters, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.database), neo4j.ExecuteQueryWithWritersRouting())
}

func (s *Store) verifyConstraints(ctx context.Context) error {
	result, err := s.query(ctx, "SHOW NODE UNIQUENESS CONSTRAINTS YIELD labelsOrTypes, properties RETURN labelsOrTypes, properties", nil)
	if err != nil {
		return fmt.Errorf("neo4j: verify uniqueness constraints: %w", err)
	}
	for _, required := range []struct {
		label      string
		properties []string
	}{
		{s.conversationLabel, []string{fieldConversationID}},
		{s.appendLabel, []string{fieldConversationID, fieldAppendID}},
	} {
		found := false
		for _, record := range result.Records {
			labels, labelsErr := recordStrings(record, "labelsOrTypes")
			properties, propertiesErr := recordStrings(record, "properties")
			if labelsErr != nil || propertiesErr != nil {
				return errors.New("neo4j: malformed uniqueness constraint response")
			}
			if len(labels) == 1 && labels[0] == required.label && sameStrings(properties, required.properties) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("neo4j: missing node uniqueness constraint for %s %v", required.label, required.properties)
		}
	}
	return nil
}

func (s *Store) Append(ctx context.Context, batch history.Batch) (history.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return history.Receipt{}, err
	}
	if err := batch.Validate(); err != nil {
		return history.Receipt{}, err
	}
	result, err := s.query(ctx, s.appendQuery, map[string]any{fieldConversationID: batch.ConversationID().String(), fieldAppendID: batch.ID().String(), fieldDigest: batch.Digest(), fieldMessages: string(batch.EncodedMessages())})
	if err != nil {
		return history.Receipt{}, fmt.Errorf("neo4j: append: %w", err)
	}
	if len(result.Records) != 1 {
		return history.Receipt{}, fmt.Errorf("neo4j: append: got %d receipts, want 1", len(result.Records))
	}
	closed, err := requiredRecordValue[bool](result.Records[0], fieldClosed)
	if err != nil {
		return history.Receipt{}, fmt.Errorf("neo4j: append: missing closure evidence: %w", err)
	}
	if closed {
		return history.Receipt{}, history.ErrConversationClosed
	}
	digest, err := requiredRecordValue[string](result.Records[0], fieldDigest)
	if err != nil {
		return history.Receipt{}, fmt.Errorf("neo4j: append: malformed receipt: %w", err)
	}
	if digest != batch.Digest() {
		return history.Receipt{}, history.ErrAppendConflict
	}
	return history.Receipt{ConversationID: batch.ConversationID(), ID: batch.ID(), Digest: digest}, nil
}

func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := conversationID.Validate(); err != nil {
		return nil, err
	}
	result, err := s.query(ctx, s.readQuery, map[string]any{fieldConversationID: conversationID.String()})
	if err != nil {
		return nil, fmt.Errorf("neo4j: read: %w", err)
	}
	messages := []chat.Message{}
	var previous int64
	for index, record := range result.Records {
		sequence, err := requiredRecordValue[int64](record, fieldSequence)
		if err != nil || sequence <= previous {
			return nil, fmt.Errorf("neo4j: read: append %d has invalid sequence %d", index, sequence)
		}
		previous = sequence
		raw, err := requiredRecordValue[string](record, fieldMessages)
		if err != nil {
			return nil, fmt.Errorf("neo4j: read: append %d has no messages: %w", index, err)
		}
		batchMessages, err := history.DecodeMessages([]byte(raw))
		if err != nil {
			return nil, fmt.Errorf("neo4j: read: append %d: %w", index, err)
		}
		messages = append(messages, batchMessages...)
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
	result, err := s.query(ctx, s.clearQuery, map[string]any{fieldConversationID: conversationID.String()})
	if err != nil {
		return fmt.Errorf("neo4j: clear: %w", err)
	}
	if closureErr := requireClosed(result); closureErr != nil {
		return fmt.Errorf("neo4j: clear: %w", closureErr)
	}
	result, err = s.query(ctx, s.cleanupQuery, map[string]any{fieldConversationID: conversationID.String()})
	if err != nil {
		return fmt.Errorf("neo4j: clear closed messages: %w", err)
	}
	return requireClosed(result)
}

func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := s.query(ctx, s.listQuery, nil)
	if err != nil {
		return nil, fmt.Errorf("neo4j: list conversations: %w", err)
	}
	ids := make([]history.ConversationID, 0, len(result.Records))
	for _, record := range result.Records {
		id, err := requiredRecordValue[string](record, fieldConversationID)
		if err != nil {
			return nil, fmt.Errorf("neo4j: list conversations: invalid ID: %w", err)
		}
		conversationID, err := history.NewConversationID(id)
		if err != nil {
			return nil, fmt.Errorf("neo4j: list conversations: %w", err)
		}
		ids = append(ids, conversationID)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
