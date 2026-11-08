package cassandra

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const (
	DefaultKeyspace           = "scope"
	DefaultTableName          = "chat_history"
	recordConversation   int8 = 0
	recordAppend         int8 = 1
	conversationRecordID      = ""
	permanentTTLSeconds       = 0
)

// Session remains caller-owned. Store sets SERIAL and QUORUM on its own
// operations because append identity must not depend on mutable client defaults.
type Session interface {
	Query(string, ...any) *gocql.Query
	Batch(gocql.BatchType) *gocql.Batch
}

type StoreConfig struct {
	Session Session
	// Replication policy belongs to the host; this keyspace must already exist.
	Keyspace         string
	TableName        string
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Session) {
		return errors.New("cassandra: session is required")
	}
	if s.Keyspace != "" && !validIdentifier(s.Keyspace) {
		return fmt.Errorf("cassandra: keyspace %q must be a valid unquoted identifier", s.Keyspace)
	}
	if s.TableName != "" && !validIdentifier(s.TableName) {
		return fmt.Errorf("cassandra: table name %q must be a valid unquoted identifier", s.TableName)
	}
	return nil
}

// Gate and append rows share one table and partition: Cassandra conditional
// batches require both, and the gate serializes appends with permanent closure.
type Store struct {
	session       Session
	createCQL     string
	initializeCQL string
	gateCQL       string
	receiptCQL    string
	advanceCQL    string
	appendCQL     string
	readCQL       string
	closeCQL      string
	clearCQL      string
	listCQL       string
}

var (
	_ Session        = (*gocql.Session)(nil)
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Keyspace == "" {
		config.Keyspace = DefaultKeyspace
	}
	if config.TableName == "" {
		config.TableName = DefaultTableName
	}
	qualified := config.Keyspace + "." + config.TableName
	// Every write overrides table TTL so host defaults cannot expire append
	// identity or reopen a permanently closed conversation.
	store := &Store{
		session: config.Session,
		createCQL: fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
            conversation_id TEXT,
            record_kind TINYINT,
            record_id TEXT,
            sequence BIGINT,
            closed BOOLEAN,
            digest TEXT,
            messages TEXT,
            PRIMARY KEY ((conversation_id), record_kind, record_id)
        )`, qualified),
		initializeCQL: fmt.Sprintf("INSERT INTO %s (conversation_id, record_kind, record_id, sequence, closed) VALUES (?, ?, ?, ?, ?) IF NOT EXISTS USING TTL %d", qualified, permanentTTLSeconds),
		gateCQL:       fmt.Sprintf("SELECT closed, sequence, WRITETIME(closed) FROM %s WHERE conversation_id = ? AND record_kind = ? AND record_id = ?", qualified),
		receiptCQL:    fmt.Sprintf("SELECT digest FROM %s WHERE conversation_id = ? AND record_kind = ? AND record_id = ?", qualified),
		advanceCQL:    fmt.Sprintf("UPDATE %s USING TTL %d SET sequence = ? WHERE conversation_id = ? AND record_kind = ? AND record_id = ? IF closed = false AND sequence = ?", qualified, permanentTTLSeconds),
		appendCQL:     fmt.Sprintf("INSERT INTO %s (conversation_id, record_kind, record_id, sequence, digest, messages) VALUES (?, ?, ?, ?, ?, ?) IF NOT EXISTS USING TTL %d", qualified, permanentTTLSeconds),
		readCQL:       fmt.Sprintf("SELECT sequence, messages FROM %s WHERE conversation_id = ? AND record_kind = ?", qualified),
		closeCQL:      fmt.Sprintf("UPDATE %s USING TTL %d SET closed = true WHERE conversation_id = ? AND record_kind = ? AND record_id = ? IF closed = false", qualified, permanentTTLSeconds),
		clearCQL:      fmt.Sprintf("DELETE FROM %s USING TIMESTAMP ? WHERE conversation_id = ? AND record_kind = ?", qualified),
		listCQL:       fmt.Sprintf("SELECT DISTINCT conversation_id FROM %s", qualified),
	}
	if config.InitializeSchema {
		if err := store.session.Query(store.createCQL).Consistency(gocql.Quorum).ExecContext(ctx); err != nil {
			return nil, fmt.Errorf("cassandra: initialize schema: %w", err)
		}
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context, id history.ConversationID, closed bool) error {
	_, err := s.session.Query(s.initializeCQL, id.String(), recordConversation, conversationRecordID, int64(0), closed).
		Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).DefaultTimestamp(false).MapScanCASContext(ctx, make(map[string]any))
	return err
}

func (s *Store) gate(ctx context.Context, id history.ConversationID) (conversationGate, error) {
	var gate conversationGate
	err := s.session.Query(s.gateCQL, id.String(), recordConversation, conversationRecordID).
		Consistency(gocql.Serial).ScanContext(ctx, &gate.closed, &gate.sequence, &gate.closedWriteTimestamp)
	if err == nil && gate.sequence < 0 {
		return conversationGate{}, errors.New("cassandra: invalid stored conversation sequence")
	}
	return gate, err
}

func (s *Store) conditionalAppend(batch history.Batch, sequence int64) *gocql.Batch {
	transaction := s.session.Batch(gocql.LoggedBatch).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).DefaultTimestamp(false)
	transaction.Query(s.advanceCQL, sequence+1, batch.ConversationID().String(), recordConversation, conversationRecordID, sequence)
	transaction.Query(s.appendCQL, batch.ConversationID().String(), recordAppend, batch.ID().String(), sequence+1, batch.Digest(), string(batch.EncodedMessages()))
	return transaction
}

func (s *Store) Append(ctx context.Context, batch history.Batch) (history.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return history.Receipt{}, err
	}
	if err := batch.Validate(); err != nil {
		return history.Receipt{}, err
	}
	if err := s.initialize(ctx, batch.ConversationID(), false); err != nil {
		return history.Receipt{}, fmt.Errorf("cassandra: append gate: %w", err)
	}
	receipt := history.Receipt{ConversationID: batch.ConversationID(), ID: batch.ID(), Digest: batch.Digest()}
	for {
		if err := ctx.Err(); err != nil {
			return history.Receipt{}, err
		}
		gate, err := s.gate(ctx, batch.ConversationID())
		if err != nil {
			return history.Receipt{}, fmt.Errorf("cassandra: append gate: %w", err)
		}
		if gate.closed {
			return history.Receipt{}, history.ErrConversationClosed
		}
		var digest string
		err = s.session.Query(s.receiptCQL, batch.ConversationID().String(), recordAppend, batch.ID().String()).Consistency(gocql.Serial).ScanContext(ctx, &digest)
		if err == nil {
			if digest != batch.Digest() {
				return history.Receipt{}, history.ErrAppendConflict
			}
			return receipt, nil
		}
		if !errors.Is(err, gocql.ErrNotFound) {
			return history.Receipt{}, fmt.Errorf("cassandra: append receipt: %w", err)
		}
		if gate.sequence == math.MaxInt64 {
			return history.Receipt{}, errors.New("cassandra: conversation sequence exhausted")
		}
		applied, iterator, err := s.conditionalAppend(batch, gate.sequence).MapExecCASContext(ctx, make(map[string]any))
		if iterator != nil {
			err = errors.Join(err, iterator.Close())
		}
		if applied {
			return receipt, err
		}
		if err != nil {
			return history.Receipt{}, fmt.Errorf("cassandra: append transaction: %w", err)
		}
	}
}

func (s *Store) Read(ctx context.Context, id history.ConversationID) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	before, err := s.gate(ctx, id)
	if errors.Is(err, gocql.ErrNotFound) || err == nil && before.closed {
		return []chat.Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cassandra: read gate: %w", err)
	}
	iterator := s.session.Query(s.readCQL, id.String(), recordAppend).Consistency(gocql.Serial).IterContext(ctx)
	var records []appendRecord
	var record appendRecord
	for iterator.Scan(&record.sequence, &record.messages) {
		records = append(records, record)
	}
	if closeErr := iterator.Close(); closeErr != nil {
		return nil, fmt.Errorf("cassandra: read appends: %w", closeErr)
	}
	after, err := s.gate(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("cassandra: read gate: %w", err)
	}
	if after.closed {
		return []chat.Message{}, nil
	}
	// A sequence bound excludes later appends across paged reads; the final gate
	// check prevents a concurrent cleanup from exposing a partial old history.
	return decodeRecords(records, before.sequence)
}

func (s *Store) Clear(ctx context.Context, id history.ConversationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := id.Validate(); err != nil {
		return err
	}
	if err := s.initialize(ctx, id, true); err != nil {
		return fmt.Errorf("cassandra: close gate: %w", err)
	}
	if _, err := s.session.Query(s.closeCQL, id.String(), recordConversation, conversationRecordID).
		Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).DefaultTimestamp(false).MapScanCASContext(ctx, make(map[string]any)); err != nil {
		return fmt.Errorf("cassandra: close conversation: %w", err)
	}
	gate, err := s.gate(ctx, id)
	if err != nil {
		return fmt.Errorf("cassandra: read closed gate: %w", err)
	}
	query, err := s.cleanupQuery(id, gate)
	if err != nil {
		return err
	}
	if err = query.ExecContext(ctx); err != nil {
		return fmt.Errorf("cassandra: clear closed conversation: %w", err)
	}
	return nil
}

func (s *Store) cleanupQuery(id history.ConversationID, gate conversationGate) (*gocql.Query, error) {
	if !gate.closed || gate.closedWriteTimestamp <= 0 {
		return nil, errors.New("cassandra: cleanup requires a committed closure timestamp")
	}
	// Paxos timestamps follow ballot order within this partition. Reusing the
	// closure timestamp deletes every earlier append even when client or
	// coordinator clocks lag; the permanent gate itself is outside this range.
	return s.session.Query(s.clearCQL, gate.closedWriteTimestamp, id.String(), recordAppend).Consistency(gocql.Quorum).DefaultTimestamp(false), nil
}

func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	iterator := s.session.Query(s.listCQL).Consistency(gocql.Quorum).IterContext(ctx)
	ids := []history.ConversationID{}
	var raw string
	for iterator.Scan(&raw) {
		ids = append(ids, history.ConversationID(raw))
	}
	if err := iterator.Close(); err != nil {
		return nil, fmt.Errorf("cassandra: list partitions: %w", err)
	}
	visible := make([]history.ConversationID, 0, len(ids))
	for _, id := range ids {
		if err := id.Validate(); err != nil {
			return nil, err
		}
		gate, err := s.gate(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("cassandra: list gate: %w", err)
		}
		if !gate.closed && gate.sequence > 0 {
			visible = append(visible, id)
		}
	}
	slices.Sort(visible)
	return visible, nil
}

type conversationGate struct {
	closed               bool
	sequence             int64
	closedWriteTimestamp int64
}

type appendRecord struct {
	sequence int64
	messages string
}

func decodeRecords(records []appendRecord, through int64) ([]chat.Message, error) {
	slices.SortFunc(records, func(left, right appendRecord) int { return cmp.Compare(left.sequence, right.sequence) })
	messages := []chat.Message{}
	previous := int64(0)
	for _, record := range records {
		if record.sequence <= previous {
			return nil, errors.New("cassandra: invalid stored append sequence")
		}
		previous = record.sequence
		if record.sequence > through {
			break
		}
		batch, err := history.DecodeMessages([]byte(record.messages))
		if err != nil {
			return nil, fmt.Errorf("cassandra: decode append: %w", err)
		}
		messages = append(messages, batch...)
	}
	return messages, nil
}
