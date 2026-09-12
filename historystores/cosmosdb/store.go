package cosmosdb

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

// StoreConfig names every dependency explicitly rather than defaulting a
// client or connection, so a store cannot be built against a service the
// caller did not choose.
type StoreConfig struct {
	// Container is the live Cosmos container handle. Required. It must be
	// partitioned on [PartitionKeyPath], which [NewStore] reads and refuses
	// with [ErrIncompatibleContainer] rather than leaving as an obligation.
	Container *azcosmos.ContainerClient
}

func (s StoreConfig) Validate() error {
	if s.Container == nil {
		return errors.New("cosmosdb: container is required")
	}
	return nil
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store persists each conversation in one Cosmos DB partition through a
// caller-owned container handle. A local monotonic sequence plus the document
// ID gives deterministic read order without relying on lossy JSON numbers;
// ordering across separate Store values remains unspecified.
type Store struct {
	container *azcosmos.ContainerClient
	sequence  *history.Sequence
}

// PartitionKeyPath is the partition-key path this store requires. Every
// document carries its conversation id there, and every read and delete is
// scoped to one partition by it.
const PartitionKeyPath = "/conversation_id"

// MaxMessagesPerWrite is Cosmos's documented ceiling on a transactional
// batch: "There's a current limit of 100 operations per transactional batch."
// One Write is one batch, so this is also the largest batch [Store.Write]
// accepts. Splitting a larger one across batches would trade the atomicity
// that makes the write reportable for a silent partial conversation, so the
// choice to accept that is left to the caller, who can make it per Write.
const MaxMessagesPerWrite = 100

// ErrIncompatibleContainer reports a container partitioned on another path.
var ErrIncompatibleContainer = errors.New("cosmosdb: container is incompatible")

// NewStore confirms the container is partitioned the way this store writes,
// which is why it takes a context. The requirement used to be an obligation
// stated in the config and checked nowhere; Cosmos rejects the first write
// itself, but that is a failure far from the wiring that chose the container.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	sequence, err := history.NewSequence(time.Nanosecond)
	if err != nil {
		return nil, err
	}

	container, err := config.Container.Read(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: read container %s: %w", config.Container.ID(), err)
	}
	if err = validatePartitionKey(container.ContainerProperties); err != nil {
		return nil, err
	}

	return &Store{container: config.Container, sequence: sequence}, nil
}

// validatePartitionKey refuses a container this store cannot address.
func validatePartitionKey(properties *azcosmos.ContainerProperties) error {
	if properties == nil {
		return fmt.Errorf("%w: the container returned no properties", ErrIncompatibleContainer)
	}
	if paths := properties.PartitionKeyDefinition.Paths; !slices.Contains(paths, PartitionKeyPath) {
		return fmt.Errorf("%w: container %s is partitioned on %v, but every document carries its conversation id at %s",
			ErrIncompatibleContainer, properties.ID, paths, PartitionKeyPath)
	}
	return nil
}

// document is the wire shape stored in Cosmos. The struct tags match
// the JSON the SDK expects.
type document struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	Sequence       string `json:"seq"`
	Message        string `json:"message"`
	CreatedAt      string `json:"created_at"`
}

// Write creates one document per message. Random document IDs prevent
// concurrent writers from overwriting each other; seq preserves argument order
// within one call. Retried calls append fresh documents and are not idempotent.
// The transactional batch either commits or rolls back; a lost acknowledgment
// returns an uncertain outcome instead of claiming that nothing was stored.
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

	if len(messages) > MaxMessagesPerWrite {
		return outcome, fmt.Errorf("cosmosdb: write: %d messages exceed the %d operations one transactional batch may carry",
			len(messages), MaxMessagesPerWrite)
	}

	encoded, err := encodeMessages(messages)
	if err != nil {
		return outcome, fmt.Errorf("cosmosdb: write: encode messages: %w", err)
	}
	partitionKey := azcosmos.NewPartitionKeyString(conversationID.String())
	sequenceBase := s.sequence.Reserve(len(encoded))
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	// Every message in one Write shares the conversation id, which is the
	// partition key, so the whole batch satisfies Cosmos's rule that "all
	// operations within a TransactionalBatch must operate on items within the
	// same partition key" -- and with it the guarantee that "if any operation
	// fails, the entire transaction is rolled back".
	batch := s.container.NewTransactionalBatch(partitionKey)
	for index, raw := range encoded {
		body, marshalErr := json.Marshal(document{
			ID:             rand.Text(),
			ConversationID: conversationID.String(),
			Sequence:       formatSequence(sequenceBase + int64(index)),
			Message:        string(raw),
			CreatedAt:      createdAt,
		})
		if marshalErr != nil {
			return outcome, fmt.Errorf("cosmosdb: write: marshal message %d: %w", index, marshalErr)
		}
		batch.CreateItem(body, nil)
	}

	outcome.Uncertain = true
	response, err := s.container.ExecuteTransactionalBatch(ctx, batch, nil)
	if err != nil {
		return outcome, fmt.Errorf("cosmosdb: write: execute batch: %w", err)
	}
	if err := writeOutcome(&response, len(encoded)); err != nil {
		outcome.Uncertain = response.Success
		return outcome, err
	}
	return history.WriteOutcome{Accepted: len(messages)}, nil
}

// writeOutcome reads what the batch actually did. Cosmos answers a rolled-back
// batch over a successful HTTP call, so Success is the only evidence the
// transaction committed; taking the call as the answer would report a
// conversation nothing stored.
func writeOutcome(response *azcosmos.TransactionalBatchResponse, operations int) error {
	if response.Success {
		if got := len(response.OperationResults); got != operations {
			return fmt.Errorf("cosmosdb: write: batch reported %d results for %d messages", got, operations)
		}
		return nil
	}
	// "The cause of the batch failure is the first operation with status code
	// different from http.StatusFailedDependency" -- every other operation
	// carries 424 to say it was rolled back rather than that it failed.
	for index, result := range response.OperationResults {
		if result.StatusCode == http.StatusFailedDependency {
			continue
		}
		return fmt.Errorf("cosmosdb: write: batch rolled back, message %d returned status %d",
			index, result.StatusCode)
	}
	return fmt.Errorf("cosmosdb: write: batch of %d message(s) rolled back without naming a cause", operations)
}

// Read returns every message stored under conversationID in
// insertion order.
func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) (storedMessages []chat.Message, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = conversationID.Validate(); err != nil {
		return nil, err
	}

	partitionKey := azcosmos.NewPartitionKeyString(conversationID.String())
	query := "SELECT c.id, c.seq, c.message FROM c WHERE c.conversation_id = @cid"
	queryOptions := &azcosmos.QueryOptions{
		QueryParameters: []azcosmos.QueryParameter{
			{Name: "@cid", Value: conversationID.String()},
		},
	}

	documents := []document{}
	pager := s.container.NewQueryItemsPager(query, partitionKey, queryOptions)
	for pager.More() {
		response, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cosmosdb: read: query page: %w", err)
		}
		for _, item := range response.Items {
			var projected document
			if err := json.Unmarshal(item, &projected); err != nil {
				return nil, fmt.Errorf("cosmosdb: read: decode document: %w", err)
			}
			if projected.ID == "" {
				return nil, errors.New("cosmosdb: read: document is missing ID")
			}
			if !validSequence(projected.Sequence) {
				return nil, fmt.Errorf("cosmosdb: read: document %q has invalid sequence %q", projected.ID, projected.Sequence)
			}
			documents = append(documents, projected)
		}
	}
	slices.SortFunc(documents, compareDocuments)
	storedMessages = make([]chat.Message, 0, len(documents))
	for index, document := range documents {
		message, err := decodeMessage([]byte(document.Message))
		if err != nil {
			return nil, fmt.Errorf("cosmosdb: read: decode message %d: %w", index, err)
		}
		storedMessages = append(storedMessages, message)
	}
	return storedMessages, nil
}

func formatSequence(sequence int64) string {
	return fmt.Sprintf("%019d", sequence)
}

func validSequence(sequence string) bool {
	value, err := strconv.ParseInt(sequence, 10, 64)
	return err == nil && value >= 0 && formatSequence(value) == sequence
}

func compareDocuments(left, right document) int {
	if order := cmp.Compare(left.Sequence, right.Sequence); order != 0 {
		return order
	}
	return cmp.Compare(left.ID, right.ID)
}

// Conversations gathers distinct IDs with a cross-partition query and returns
// them in lexical order.
func (s *Store) Conversations(ctx context.Context) (ids []history.ConversationID, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	// The Go SDK's gateway supports cross-partition projections, but not
	// DISTINCT. Uniqueness is established after all pages have been read.
	query := "SELECT VALUE c.conversation_id FROM c"

	ids = []history.ConversationID{}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKey(), nil)
	for pager.More() {
		response, pageErr := pager.NextPage(ctx)
		if pageErr != nil {
			return nil, fmt.Errorf("cosmosdb: list conversations: query page: %w", pageErr)
		}
		for _, item := range response.Items {
			var id string
			if err = json.Unmarshal(item, &id); err != nil {
				return nil, fmt.Errorf("cosmosdb: list conversations: decode ID: %w", err)
			}
			conversationID := history.ConversationID(id)
			if err = conversationID.Validate(); err != nil {
				return nil, fmt.Errorf("cosmosdb: list conversations: invalid stored ID %q: %w", id, err)
			}
			ids = append(ids, conversationID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// Clear deletes every document for conversationID. Cosmos has no
// bulk-delete for a partition, so each id is enumerated and
// deleted individually — fine for chat history sizes.
func (s *Store) Clear(ctx context.Context, conversationID history.ConversationID) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = conversationID.Validate(); err != nil {
		return err
	}

	partitionKey := azcosmos.NewPartitionKeyString(conversationID.String())
	query := "SELECT c.id FROM c WHERE c.conversation_id = @cid"
	queryOptions := &azcosmos.QueryOptions{
		QueryParameters: []azcosmos.QueryParameter{
			{Name: "@cid", Value: conversationID.String()},
		},
	}

	// Deleting while paging the same query can skip items (the
	// continuation token is computed against the mutating result set),
	// so each round re-runs the query from scratch and deletes one
	// non-empty page, until the query is exhausted. Empty pages can
	// carry continuation tokens and do not establish absence.
	for {
		pager := s.container.NewQueryItemsPager(query, partitionKey, queryOptions)
		var response azcosmos.QueryItemsResponse
		for pager.More() {
			response, err = pager.NextPage(ctx)
			if err != nil {
				return fmt.Errorf("cosmosdb: clear: query document IDs: %w", err)
			}
			if len(response.Items) > 0 {
				break
			}
		}
		if len(response.Items) == 0 {
			return nil
		}
		for _, item := range response.Items {
			var projected struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(item, &projected); err != nil {
				return fmt.Errorf("cosmosdb: clear: decode document ID: %w", err)
			}
			if _, err := s.container.DeleteItem(ctx, partitionKey, projected.ID, nil); err != nil {
				return fmt.Errorf("cosmosdb: clear: delete document %q: %w", projected.ID, err)
			}
		}
	}
}
