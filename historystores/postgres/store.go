package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const (
	DefaultSchemaName       = "public"
	DefaultTableName        = "chat_history"
	DefaultIndexNameSuffix  = "_conversation_idx"
	conversationTableSuffix = "_conversations"
)

type StoreConfig struct {
	Pool                  *pgxpool.Pool
	SchemaName            string
	TableName             string
	ConversationTableName string
	IndexName             string
	InitializeSchema      bool
}

func (s StoreConfig) resolved() StoreConfig {
	s.SchemaName = cmp.Or(s.SchemaName, DefaultSchemaName)
	s.TableName = cmp.Or(s.TableName, DefaultTableName)
	s.ConversationTableName = cmp.Or(s.ConversationTableName, s.TableName+conversationTableSuffix)
	if s.InitializeSchema {
		s.IndexName = cmp.Or(s.IndexName, s.TableName+DefaultIndexNameSuffix)
	}
	return s
}

func (s StoreConfig) Validate() error {
	effective := s.resolved()
	if effective.Pool == nil {
		return errors.New("postgres: pool is required")
	}
	for _, identifier := range [...]struct{ name, value string }{
		{"schema name", effective.SchemaName},
		{"table name", effective.TableName},
		{"conversation table name", effective.ConversationTableName},
		{"index name", effective.IndexName},
	} {
		if identifier.value != "" && !validIdentifier(identifier.value) {
			return fmt.Errorf("postgres: %s %q must be a valid unquoted identifier of at most %d bytes", identifier.name, identifier.value, maxIdentifierBytes)
		}
	}
	if effective.TableName == effective.ConversationTableName {
		return errors.New("postgres: append and conversation tables must be distinct")
	}
	return nil
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// The conversation row serializes append and closure across every Store using
// this schema. Receipts survive payload removal, so an acknowledgment loss can
// never turn a retry into a second append.
type Store struct {
	pool                  *pgxpool.Pool
	readSQL               string
	appendSQL             string
	ensureConversationSQL string
	lockConversationSQL   string
	receiptSQL            string
	closeSQL              string
	discardMessagesSQL    string
	listSQL               string
	schemaSQL             string
	createSQL             []string
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.resolved()
	qualified := pgx.Identifier{config.SchemaName, config.TableName}.Sanitize()
	conversations := pgx.Identifier{config.SchemaName, config.ConversationTableName}.Sanitize()
	s := &Store{
		pool:                  config.Pool,
		readSQL:               fmt.Sprintf("SELECT a.messages FROM %s AS a JOIN %s AS c USING (conversation_id) WHERE a.conversation_id = $1 AND NOT c.closed ORDER BY a.seq", qualified, conversations),
		appendSQL:             fmt.Sprintf("INSERT INTO %s (conversation_id, append_id, digest, messages) VALUES ($1, $2, $3, $4::bytea)", qualified),
		ensureConversationSQL: fmt.Sprintf("INSERT INTO %s (conversation_id, closed) VALUES ($1, false) ON CONFLICT (conversation_id) DO NOTHING", conversations),
		lockConversationSQL:   fmt.Sprintf("SELECT closed FROM %s WHERE conversation_id = $1 FOR UPDATE", conversations),
		receiptSQL:            fmt.Sprintf("SELECT digest FROM %s WHERE conversation_id = $1 AND append_id = $2", qualified),
		closeSQL:              fmt.Sprintf("UPDATE %s SET closed = true WHERE conversation_id = $1", conversations),
		discardMessagesSQL:    fmt.Sprintf("UPDATE %s SET messages = NULL WHERE conversation_id = $1", qualified),
		listSQL:               fmt.Sprintf("SELECT conversation_id FROM %s WHERE NOT closed", conversations),
		schemaSQL:             fmt.Sprintf("SELECT a.messages FROM %s AS a JOIN %s AS c USING (conversation_id) WHERE a.append_id IS NOT NULL AND a.digest IS NOT NULL AND NOT c.closed LIMIT 0", qualified, conversations),
		createSQL: []string{
			fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", pgx.Identifier{config.SchemaName}.Sanitize()),
			fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (conversation_id TEXT PRIMARY KEY, closed BOOLEAN NOT NULL)", conversations),
			fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (seq BIGSERIAL PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES %s (conversation_id), append_id TEXT NOT NULL, digest TEXT NOT NULL, messages BYTEA, UNIQUE (conversation_id, append_id))", qualified, conversations),
			fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (conversation_id, seq)", pgx.Identifier{config.IndexName}.Sanitize(), qualified),
		},
	}
	if config.InitializeSchema {
		for _, statement := range s.createSQL {
			if _, err := s.pool.Exec(ctx, statement); err != nil {
				return nil, fmt.Errorf("postgres: initialize schema: %w", err)
			}
		}
	}
	rows, err := s.pool.Query(ctx, s.schemaSQL)
	if err != nil {
		return nil, fmt.Errorf("postgres: verify append schema: %w", err)
	}
	defer rows.Close()
	if err := s.validateMessagesColumn(rows); err != nil {
		return nil, err
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: verify append schema: %w", err)
	}
	return s, nil
}

func (s *Store) validateMessagesColumn(rows pgx.Rows) error {
	fields := rows.FieldDescriptions()
	if len(fields) != 1 || fields[0].DataTypeOID != pgtype.ByteaOID {
		return errors.New("postgres: messages column must be bytea containing canonical Core wire bytes")
	}
	return nil
}

func (s *Store) lockConversation(ctx context.Context, transaction pgx.Tx, id history.ConversationID) (bool, error) {
	if _, err := transaction.Exec(ctx, s.ensureConversationSQL, id.String()); err != nil {
		return false, err
	}
	var closed bool
	if err := transaction.QueryRow(ctx, s.lockConversationSQL, id.String()).Scan(&closed); err != nil {
		return false, err
	}
	return closed, nil
}

func (s *Store) Append(ctx context.Context, batch history.Batch) (receipt history.Receipt, err error) {
	if err = ctx.Err(); err != nil {
		return receipt, err
	}
	if err = batch.Validate(); err != nil {
		return receipt, err
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return receipt, fmt.Errorf("postgres: append: begin transaction: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("postgres: append: rollback: %w", rollbackErr))
		}
	}()
	closed, err := s.lockConversation(ctx, transaction, batch.ConversationID())
	if err != nil {
		return receipt, fmt.Errorf("postgres: append: lock conversation: %w", err)
	}
	if closed {
		return receipt, history.ErrConversationClosed
	}
	var digest string
	err = transaction.QueryRow(ctx, s.receiptSQL, batch.ConversationID().String(), batch.ID().String()).Scan(&digest)
	switch {
	case err == nil:
		if digest != batch.Digest() {
			return receipt, history.ErrAppendConflict
		}
		return batch.Receipt(), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return receipt, fmt.Errorf("postgres: append: read receipt: %w", err)
	}
	tag, err := transaction.Exec(ctx, s.appendSQL, batch.ConversationID().String(), batch.ID().String(), batch.Digest(), []byte(batch.EncodedMessages()))
	if err != nil {
		return receipt, fmt.Errorf("postgres: append: insert batch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return receipt, fmt.Errorf("postgres: append: expected one inserted batch, got %d", tag.RowsAffected())
	}
	if err = transaction.Commit(ctx); err != nil {
		return receipt, fmt.Errorf("postgres: append: commit: %w", err)
	}
	return batch.Receipt(), nil
}

func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := conversationID.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, s.readSQL, conversationID.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: read: query: %w", err)
	}
	defer rows.Close()
	if err := s.validateMessagesColumn(rows); err != nil {
		return nil, err
	}
	storedMessages := []chat.Message{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("postgres: read: scan batch: %w", err)
		}
		messages, err := history.DecodeMessages(raw)
		if err != nil {
			return nil, fmt.Errorf("postgres: read: decode batch: %w", err)
		}
		storedMessages = append(storedMessages, messages...)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: read: iterate rows: %w", err)
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
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres: clear: begin transaction: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("postgres: clear: rollback: %w", rollbackErr))
		}
	}()
	if _, lockErr := s.lockConversation(ctx, transaction, conversationID); lockErr != nil {
		return fmt.Errorf("postgres: clear: lock conversation: %w", lockErr)
	}
	tag, err := transaction.Exec(ctx, s.closeSQL, conversationID.String())
	if err != nil {
		return fmt.Errorf("postgres: clear: close conversation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("postgres: clear: expected one closed conversation, got %d", tag.RowsAffected())
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: clear: commit: %w", err)
	}
	if _, err := s.pool.Exec(ctx, s.discardMessagesSQL, conversationID.String()); err != nil {
		return fmt.Errorf("postgres: clear closed messages: %w", err)
	}
	return nil
}

func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, s.listSQL)
	if err != nil {
		return nil, fmt.Errorf("postgres: list conversations: query: %w", err)
	}
	defer rows.Close()
	ids := []history.ConversationID{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: list conversations: scan ID: %w", err)
		}
		conversationID := history.ConversationID(id)
		if err := conversationID.Validate(); err != nil {
			return nil, fmt.Errorf("postgres: list conversations: invalid stored ID %q: %w", id, err)
		}
		ids = append(ids, conversationID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list conversations: iterate rows: %w", err)
	}
	slices.Sort(ids)
	return ids, nil
}
