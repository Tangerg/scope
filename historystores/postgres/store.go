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

// Default identifiers used when [StoreConfig] leaves them blank.
const (
	DefaultSchemaName      = "public"
	DefaultTableName       = "chat_history"
	DefaultIndexNameSuffix = "_conversation_idx"
)

// StoreConfig names every dependency explicitly rather than defaulting a
// client or connection, so a store cannot be built against a service the
// caller did not choose.
type StoreConfig struct {
	// Pool is the pgx connection pool. Required. The store does not
	// take ownership — callers close the pool themselves.
	Pool *pgxpool.Pool

	// SchemaName is the PostgreSQL schema that holds the chat history
	// table. Optional: defaults to [DefaultSchemaName] ("public").
	SchemaName string

	// TableName is the table that stores serialized messages.
	// Optional: defaults to [DefaultTableName] ("chat_history").
	TableName string

	// IndexName overrides the conversation-id index name generated
	// when InitializeSchema is true. Optional: defaults to
	// "<TableName><DefaultIndexNameSuffix>".
	IndexName string

	// InitializeSchema, when true, creates the table and index if
	// they don't already exist. When false the store assumes the
	// schema is already provisioned. Both paths verify the message column
	// stores canonical Core wire bytes as BYTEA.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	if s.Pool == nil {
		return errors.New("postgres: pool is required")
	}
	for _, identifier := range [...]struct {
		name  string
		value string
	}{
		{name: "schema name", value: s.SchemaName},
		{name: "table name", value: s.TableName},
		{name: "index name", value: s.IndexName},
	} {
		if identifier.value != "" && !validIdentifier(identifier.value) {
			return fmt.Errorf("postgres: %s %q must be a valid unquoted identifier", identifier.name, identifier.value)
		}
	}
	return nil
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store persists canonical chat messages through a caller-owned pgx pool. It
// never closes the pool; schema initialization is optional and idempotent.
//
// Schema (created when [StoreConfig.InitializeSchema] is true):
//
//	CREATE TABLE <schema>.<table> (
//	    seq             BIGSERIAL    PRIMARY KEY,
//	    conversation_id TEXT         NOT NULL,
//	    message         BYTEA        NOT NULL,
//	    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
//	);
//	CREATE INDEX <index>
//	    ON <schema>.<table> (conversation_id, seq);
//
// `seq` is global (BIGSERIAL) so concurrent writers in different
// conversations don't contend on a per-conversation counter; ordering
// inside a single conversation is recovered by ORDER BY seq.
type Store struct {
	pool *pgxpool.Pool

	// Pre-formatted SQL — interpolated identifiers are validated at
	// construction time so the hot path is plain parameter binding.
	readSQL   string
	writeSQL  string
	clearSQL  string
	listSQL   string
	schemaSQL string
	createSQL []string
}

// NewStore verifies the binary message column even when the Host provisions
// the schema. JSONB cannot preserve the full Core wire vocabulary.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.SchemaName = cmp.Or(config.SchemaName, DefaultSchemaName)
	config.TableName = cmp.Or(config.TableName, DefaultTableName)
	config.IndexName = cmp.Or(config.IndexName, config.TableName+DefaultIndexNameSuffix)

	qualified := config.SchemaName + "." + config.TableName
	s := &Store{
		pool: config.Pool,
		readSQL: fmt.Sprintf(
			"SELECT message FROM %s WHERE conversation_id = $1 ORDER BY seq",
			qualified,
		),
		writeSQL: fmt.Sprintf(
			"INSERT INTO %s (conversation_id, message) VALUES ($1, $2::bytea)",
			qualified,
		),
		clearSQL: fmt.Sprintf(
			"DELETE FROM %s WHERE conversation_id = $1",
			qualified,
		),
		listSQL: fmt.Sprintf(
			"SELECT DISTINCT conversation_id FROM %s",
			qualified,
		),
		schemaSQL: "SELECT message FROM " + qualified + " LIMIT 0",
		createSQL: []string{
			fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, config.SchemaName),
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				seq             BIGSERIAL    PRIMARY KEY,
				conversation_id TEXT         NOT NULL,
				message         BYTEA        NOT NULL,
				created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
			)`, qualified),
			fmt.Sprintf(
				`CREATE INDEX IF NOT EXISTS %s ON %s (conversation_id, seq)`,
				config.IndexName, qualified,
			),
		},
	}

	if config.InitializeSchema {
		if err := s.initSchema(ctx); err != nil {
			return nil, fmt.Errorf("postgres: initialize schema: %w", err)
		}
	}
	rows, err := s.pool.Query(ctx, s.schemaSQL)
	if err != nil {
		return nil, fmt.Errorf("postgres: verify message column: %w", err)
	}
	defer rows.Close()
	if err := s.validateMessageColumn(rows); err != nil {
		return nil, err
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: verify message column: %w", err)
	}

	return s, nil
}

func (s *Store) validateMessageColumn(rows pgx.Rows) error {
	fields := rows.FieldDescriptions()
	if len(fields) != 1 || fields[0].DataTypeOID != pgtype.ByteaOID {
		return errors.New("postgres: message column must be bytea containing canonical Core wire bytes")
	}
	return nil
}

// initSchema creates the table + index if they don't exist. Idempotent.
func (s *Store) initSchema(ctx context.Context) error {
	for _, statement := range s.createSQL {
		if _, err := s.pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Write validates the complete batch, checks each native insert acknowledgment,
// and commits all messages together. Empty writes are a no-op.
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
		return outcome, fmt.Errorf("postgres: write: encode messages: %w", err)
	}
	batch := &pgx.Batch{}
	for _, raw := range encoded {
		batch.Queue(s.writeSQL, conversationID.String(), raw)
	}

	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return outcome, fmt.Errorf("postgres: write: begin transaction: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("postgres: write: rollback: %w", rollbackErr))
		}
	}()
	results := transaction.SendBatch(ctx, batch)
	for index := range encoded {
		tag, execErr := results.Exec()
		if execErr != nil {
			err = execErr
			break
		}
		if tag.RowsAffected() != 1 {
			err = fmt.Errorf("message %d: expected one inserted row, got %d", index, tag.RowsAffected())
			break
		}
	}
	err = errors.Join(err, results.Close())
	if err != nil {
		return outcome, fmt.Errorf("postgres: write: execute batch: %w", err)
	}
	outcome.Uncertain = true
	if err = transaction.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			outcome = history.WriteOutcome{}
		}
		return outcome, fmt.Errorf("postgres: write: commit: %w", err)
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

	rows, err := s.pool.Query(ctx, s.readSQL, conversationID.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: read: query: %w", err)
	}
	defer rows.Close()
	if err := s.validateMessageColumn(rows); err != nil {
		return nil, err
	}

	storedMessages = []chat.Message{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("postgres: read: scan message: %w", err)
		}
		message, err := decodeMessage(raw)
		if err != nil {
			return nil, fmt.Errorf("postgres: read: decode message: %w", err)
		}
		storedMessages = append(storedMessages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: read: iterate rows: %w", err)
	}
	return storedMessages, nil
}

// Clear drops every message stored under conversationID. Unknown ids
// are silently ignored.
func (s *Store) Clear(ctx context.Context, conversationID history.ConversationID) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = conversationID.Validate(); err != nil {
		return err
	}

	if _, err = s.pool.Exec(ctx, s.clearSQL, conversationID.String()); err != nil {
		return fmt.Errorf("postgres: clear: delete messages: %w", err)
	}
	return nil
}

// Conversations returns every stored conversation ID in lexical order.
func (s *Store) Conversations(ctx context.Context) (ids []history.ConversationID, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, s.listSQL)
	if err != nil {
		return nil, fmt.Errorf("postgres: list conversations: query: %w", err)
	}
	defer rows.Close()

	ids = []history.ConversationID{}
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
