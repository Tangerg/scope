package cassandra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gocql/gocql"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const (
	DefaultKeyspace  = "scope"
	DefaultTableName = "chat_history"
)

type StoreConfig struct {
	Session *gocql.Session

	// Keyspace must already exist: Cassandra keyspace creation needs
	// replication-strategy choices the store cannot make for the caller.
	Keyspace string

	TableName string

	// InitializeSchema creates the table only; the keyspace is never created.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	if s.Session == nil {
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

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store persists each conversation in one Cassandra partition through a
// caller-owned session. It never closes that session. A Write preserves its
// argument order with a locally monotonic TIMEUUID range; relative ordering
// across concurrent calls or separate Store values remains unspecified.
type Store struct {
	session  *gocql.Session
	sequence *history.Sequence

	writeCQL  string
	readCQL   string
	clearCQL  string
	listCQL   string
	createCQL string
}

// ErrUnacknowledgedWrites reports a session whose consistency level lets
// Cassandra accept a write without any replica holding it.
var ErrUnacknowledgedWrites = errors.New("cassandra: session consistency does not acknowledge a durable write")

// requireDurableConsistency refuses a session configured at ANY.
//
// At every other level a successful write reached at least one replica. At ANY,
// Cassandra documents that "a single replica may respond, or the coordinator
// may store a hint. If a hint is stored, the coordinator will later attempt to
// replay the hint and deliver the mutation to the replicas" — so the call
// returns nil for a message no replica holds, which no read can see and which
// is gone if the hint expires before delivery. That is a stored conversation
// reported to a caller who has not got one.
//
// The level is read off a batch rather than the session, because that is the
// only exported path to it; building one performs no I/O and NewBatch copies
// the session's own value.
func requireDurableConsistency(session *gocql.Session) error {
	if consistency := session.NewBatch(gocql.UnloggedBatch).GetConsistency(); consistency == gocql.Any {
		return fmt.Errorf("%w: consistency is %s", ErrUnacknowledgedWrites, consistency)
	}
	return nil
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := requireDurableConsistency(config.Session); err != nil {
		return nil, err
	}
	if config.Keyspace == "" {
		config.Keyspace = DefaultKeyspace
	}
	if config.TableName == "" {
		config.TableName = DefaultTableName
	}
	qualified := config.Keyspace + "." + config.TableName
	sequence, err := history.NewSequence(timeUUIDTick)
	if err != nil {
		return nil, err
	}
	s := &Store{
		session:  config.Session,
		sequence: sequence,
		writeCQL: fmt.Sprintf(
			"INSERT INTO %s (conversation_id, seq, message) VALUES (?, ?, ?)",
			qualified,
		),
		readCQL: fmt.Sprintf(
			"SELECT message FROM %s WHERE conversation_id = ? ORDER BY seq ASC",
			qualified,
		),
		clearCQL: fmt.Sprintf("DELETE FROM %s WHERE conversation_id = ?", qualified),
		listCQL:  fmt.Sprintf("SELECT DISTINCT conversation_id FROM %s", qualified),
		createCQL: fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			conversation_id TEXT,
			seq             TIMEUUID,
			message         TEXT,
			PRIMARY KEY ((conversation_id), seq)
		) WITH CLUSTERING ORDER BY (seq ASC)`, qualified),
	}

	if config.InitializeSchema {
		if err := s.session.Query(s.createCQL).WithContext(ctx).Exec(); err != nil {
			return nil, fmt.Errorf("cassandra: create table: %w", err)
		}
	}

	return s, nil
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
		return outcome, fmt.Errorf("cassandra: write: encode messages: %w", err)
	}
	batch := s.session.NewBatch(gocql.UnloggedBatch).WithContext(ctx)
	sequenceBase := s.sequence.Reserve(len(encoded))
	for index, raw := range encoded {
		messageSequence := sequenceUUID(sequenceBase, index)
		batch.Query(s.writeCQL, conversationID.String(), messageSequence, string(raw))
	}
	outcome.Uncertain = true
	if err = s.session.ExecuteBatch(batch); err != nil {
		return outcome, fmt.Errorf("cassandra: write: execute batch: %w", err)
	}
	return history.WriteOutcome{Accepted: len(messages)}, nil
}

func sequenceUUID(base int64, index int) gocql.UUID {
	return gocql.UUIDFromTime(time.Unix(0, base+int64(index)*int64(timeUUIDTick)))
}

func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) (storedMessages []chat.Message, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = conversationID.Validate(); err != nil {
		return nil, err
	}

	iterator := s.session.Query(s.readCQL, conversationID.String()).WithContext(ctx).Iter()
	defer closeIterator(iterator, "read", &err)

	storedMessages = []chat.Message{}
	var encodedMessage string
	for iterator.Scan(&encodedMessage) {
		message, decodeErr := decodeMessage([]byte(encodedMessage))
		if decodeErr != nil {
			err = fmt.Errorf("cassandra: read: decode message %d: %w", len(storedMessages), decodeErr)
			return nil, err
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

	if err = s.session.Query(s.clearCQL, conversationID.String()).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("cassandra: clear: delete partition: %w", err)
	}
	return nil
}

// SELECT DISTINCT on the partition key reads only partition metadata,
// so no ALLOW FILTERING is needed.
func (s *Store) Conversations(ctx context.Context) (ids []history.ConversationID, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	iterator := s.session.Query(s.listCQL).WithContext(ctx).Iter()
	defer closeIterator(iterator, "list conversations", &err)

	ids = []history.ConversationID{}
	var id string
	for iterator.Scan(&id) {
		conversationID := history.ConversationID(id)
		if err := conversationID.Validate(); err != nil {
			return nil, fmt.Errorf("cassandra: list conversations: invalid stored ID %q: %w", id, err)
		}
		ids = append(ids, conversationID)
	}
	slices.Sort(ids)
	return ids, nil
}

func closeIterator(iterator *gocql.Iter, operation string, operationErr *error) {
	if err := iterator.Close(); err != nil {
		*operationErr = errors.Join(*operationErr, fmt.Errorf("cassandra: %s: close iterator: %w", operation, err))
	}
}
