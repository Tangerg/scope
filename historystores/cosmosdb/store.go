package cosmosdb

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const PartitionKeyPath = "/conversation_id"
const requiredConsistency = azcosmos.ConsistencyLevelStrong

var ErrIncompatibleContainer = errors.New("cosmosdb: container is incompatible")

type StoreConfig struct{ Container *azcosmos.ContainerClient }

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

// The server's gate ETag owns admission. Neither process-local sequence numbers
// nor a successful transport call can acknowledge a conditional transaction.
type Store struct{ container *azcosmos.ContainerClient }

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	container, err := config.Container.Read(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cosmosdb: read container: %w", err)
	}
	if err := validatePartitionKey(container.ContainerProperties); err != nil {
		return nil, err
	}
	consistency := requiredConsistency
	pager := config.Container.NewQueryItemsPager("SELECT VALUE c.id FROM c WHERE false", azcosmos.NewPartitionKey(), &azcosmos.QueryOptions{ConsistencyLevel: &consistency})
	if _, err := pager.NextPage(ctx); err != nil {
		return nil, fmt.Errorf("%w: strong consistency is required: %w", ErrIncompatibleContainer, err)
	}
	return &Store{container: config.Container}, nil
}

func validatePartitionKey(properties *azcosmos.ContainerProperties) error {
	if properties == nil {
		return fmt.Errorf("%w: the container returned no properties", ErrIncompatibleContainer)
	}
	if properties.PartitionKeyDefinition.Kind != azcosmos.PartitionKeyKindHash {
		return fmt.Errorf("%w: partition key kind must be Hash, got %q", ErrIncompatibleContainer, properties.PartitionKeyDefinition.Kind)
	}
	if paths := properties.PartitionKeyDefinition.Paths; len(paths) != 1 || paths[0] != PartitionKeyPath {
		return fmt.Errorf("%w: partition key must be %s, got %v", ErrIncompatibleContainer, PartitionKeyPath, paths)
	}
	return nil
}

func (s *Store) readConversation(ctx context.Context, id history.ConversationID) (conversationDocument, azcore.ETag, bool, error) {
	response, err := s.container.ReadItem(ctx, azcosmos.NewPartitionKeyString(id.String()), string(conversationKind), strongItemOptions())
	if responseStatus(err) == http.StatusNotFound {
		return newConversationDocument(id), "", false, nil
	}
	if err != nil {
		return conversationDocument{}, "", false, fmt.Errorf("cosmosdb: read conversation: %w", err)
	}
	var document conversationDocument
	if err := jsonv2.Unmarshal(response.Value, &document); err != nil {
		return document, "", false, fmt.Errorf("cosmosdb: decode conversation: %w", err)
	}
	if err := document.validate(id); err != nil {
		return document, "", false, err
	}
	if response.ETag == "" {
		return document, "", false, errors.New("cosmosdb: conversation has no ETag")
	}
	return document, response.ETag, true, nil
}

func (s *Store) readAppend(ctx context.Context, batch history.Batch, id string) (appendDocument, bool, error) {
	response, err := s.container.ReadItem(ctx, azcosmos.NewPartitionKeyString(batch.ConversationID().String()), id, strongItemOptions())
	if responseStatus(err) == http.StatusNotFound {
		return appendDocument{}, false, nil
	}
	if err != nil {
		return appendDocument{}, false, fmt.Errorf("cosmosdb: read receipt: %w", err)
	}
	var document appendDocument
	if err := jsonv2.Unmarshal(response.Value, &document); err != nil {
		return document, false, fmt.Errorf("cosmosdb: decode receipt: %w", err)
	}
	if err := document.validate(batch.ConversationID()); err != nil {
		return document, false, err
	}
	if document.AppendID != batch.ID().String() {
		return document, false, errors.New("cosmosdb: receipt belongs to a different append")
	}
	return document, true, nil
}

func (s *Store) apply(ctx context.Context, batch azcosmos.TransactionalBatch, operations int) (bool, error) {
	consistency := requiredConsistency
	response, err := s.container.ExecuteTransactionalBatch(ctx, batch, &azcosmos.TransactionalBatchOptions{ConsistencyLevel: &consistency})
	if err != nil {
		return false, fmt.Errorf("cosmosdb: execute transaction: %w", err)
	}
	if len(response.OperationResults) != operations {
		return false, fmt.Errorf("cosmosdb: transaction returned %d results for %d operations", len(response.OperationResults), operations)
	}
	if response.Success {
		for index, result := range response.OperationResults {
			if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
				return false, fmt.Errorf("cosmosdb: committed transaction has invalid status %d at operation %d", result.StatusCode, index)
			}
		}
		return true, nil
	}
	failureIndex := -1
	for index, result := range response.OperationResults {
		if result.StatusCode == http.StatusFailedDependency {
			continue
		}
		if failureIndex >= 0 {
			return false, errors.New("cosmosdb: rollback named more than one failed operation")
		}
		failureIndex = index
	}
	if failureIndex < 0 {
		return false, errors.New("cosmosdb: transaction rolled back without a cause")
	}
	status := response.OperationResults[failureIndex].StatusCode
	if status == http.StatusConflict || status == http.StatusPreconditionFailed {
		return false, nil
	}
	return false, fmt.Errorf("cosmosdb: transaction rolled back at operation %d with status %d", failureIndex, status)
}

func (s *Store) Append(ctx context.Context, batch history.Batch) (history.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return history.Receipt{}, err
	}
	if err := batch.Validate(); err != nil {
		return history.Receipt{}, err
	}
	id, err := appendDocumentID(batch.ID())
	if err != nil {
		return history.Receipt{}, err
	}
	payload := appendDocument{ID: id, Kind: appendKind, ConversationID: batch.ConversationID().String(), AppendID: batch.ID().String(), Digest: batch.Digest(), Messages: string(batch.EncodedMessages()), Sequence: formatSequence(0), TTL: permanentTTL}
	body, err := jsonv2.Marshal(payload)
	if err != nil {
		return history.Receipt{}, fmt.Errorf("cosmosdb: encode append: %w", err)
	}
	if len(body) > maximumAppendDocumentBytes {
		return history.Receipt{}, fmt.Errorf("cosmosdb: append document exceeds %d bytes", maximumAppendDocumentBytes)
	}
	partition := azcosmos.NewPartitionKeyString(batch.ConversationID().String())
	for {
		if err := ctx.Err(); err != nil {
			return history.Receipt{}, err
		}
		conversation, etag, exists, err := s.readConversation(ctx, batch.ConversationID())
		if err != nil {
			return history.Receipt{}, err
		}
		if conversation.closed() {
			return history.Receipt{}, history.ErrConversationClosed
		}
		stored, duplicate, err := s.readAppend(ctx, batch, id)
		if err != nil {
			return history.Receipt{}, err
		}
		if !duplicate {
			conversation, err = conversation.advance()
			if err != nil {
				return history.Receipt{}, err
			}
		}
		gate, err := jsonv2.Marshal(conversation)
		if err != nil {
			return history.Receipt{}, fmt.Errorf("cosmosdb: encode conversation: %w", err)
		}
		transaction := s.container.NewTransactionalBatch(partition)
		if exists {
			transaction.ReplaceItem(conversation.ID, gate, &azcosmos.TransactionalBatchItemOptions{IfMatchETag: &etag})
		} else {
			transaction.CreateItem(gate, nil)
		}
		operations := 1
		if !duplicate {
			payload.Sequence = conversation.LastSequence
			body, err = jsonv2.Marshal(payload)
			if err != nil {
				return history.Receipt{}, fmt.Errorf("cosmosdb: encode append: %w", err)
			}
			transaction.CreateItem(body, nil)
			operations++
		}
		applied, err := s.apply(ctx, transaction, operations)
		if err != nil {
			return history.Receipt{}, err
		}
		if !applied {
			continue
		}
		if duplicate && stored.Digest != batch.Digest() {
			return history.Receipt{}, history.ErrAppendConflict
		}
		return history.Receipt{ConversationID: batch.ConversationID(), ID: batch.ID(), Digest: batch.Digest()}, nil
	}
}

func (s *Store) Read(ctx context.Context, id history.ConversationID) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	conversation, _, exists, conversationErr := s.readConversation(ctx, id)
	if conversationErr != nil {
		return nil, conversationErr
	}
	if !exists || conversation.closed() {
		return []chat.Message{}, nil
	}
	consistency := requiredConsistency
	options := &azcosmos.QueryOptions{ConsistencyLevel: &consistency, QueryParameters: []azcosmos.QueryParameter{{Name: "@kind", Value: appendKind}, {Name: "@sequence", Value: conversation.LastSequence}}}
	pager := s.container.NewQueryItemsPager("SELECT * FROM c WHERE c.kind = @kind AND c.seq <= @sequence", azcosmos.NewPartitionKeyString(id.String()), options)
	documents := []appendDocument{}
	for pager.More() {
		response, pageErr := pager.NextPage(ctx)
		if pageErr != nil {
			return nil, fmt.Errorf("cosmosdb: read append page: %w", pageErr)
		}
		for _, item := range response.Items {
			var document appendDocument
			if err := jsonv2.Unmarshal(item, &document); err != nil {
				return nil, fmt.Errorf("cosmosdb: decode append: %w", err)
			}
			if err := document.validate(id); err != nil {
				return nil, err
			}
			documents = append(documents, document)
		}
	}
	current, _, exists, closureErr := s.readConversation(ctx, id)
	if closureErr != nil {
		return nil, closureErr
	}
	if !exists {
		return nil, errors.New("cosmosdb: conversation gate disappeared")
	}
	if current.closed() {
		return []chat.Message{}, nil
	}
	slices.SortFunc(documents, func(left, right appendDocument) int { return cmp.Compare(left.Sequence, right.Sequence) })
	messages := []chat.Message{}
	for index, document := range documents {
		if document.Sequence != formatSequence(int64(index)+1) {
			return nil, errors.New("cosmosdb: stored append sequence has a gap or duplicate")
		}
		decoded, err := history.DecodeMessages([]byte(document.Messages))
		if err != nil {
			return nil, fmt.Errorf("cosmosdb: decode messages: %w", err)
		}
		messages = append(messages, decoded...)
	}
	if formatSequence(int64(len(documents))) != conversation.LastSequence {
		return nil, errors.New("cosmosdb: stored append snapshot is incomplete")
	}
	return messages, nil
}

func (s *Store) Clear(ctx context.Context, id history.ConversationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := id.Validate(); err != nil {
		return err
	}
	partition := azcosmos.NewPartitionKeyString(id.String())
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conversation, etag, exists, err := s.readConversation(ctx, id)
		if err != nil {
			return err
		}
		if conversation.closed() {
			break
		}
		body, err := jsonv2.Marshal(conversation.close())
		if err != nil {
			return fmt.Errorf("cosmosdb: encode closure: %w", err)
		}
		transaction := s.container.NewTransactionalBatch(partition)
		if exists {
			transaction.ReplaceItem(conversation.ID, body, &azcosmos.TransactionalBatchItemOptions{IfMatchETag: &etag})
		} else {
			transaction.CreateItem(body, nil)
		}
		applied, err := s.apply(ctx, transaction, 1)
		if err != nil {
			return err
		}
		if applied {
			break
		}
	}
	return s.removeClosedPayloads(ctx, id)
}

func (s *Store) removeClosedPayloads(ctx context.Context, id history.ConversationID) error {
	partition := azcosmos.NewPartitionKeyString(id.String())
	consistency := requiredConsistency
	options := &azcosmos.QueryOptions{ConsistencyLevel: &consistency, QueryParameters: []azcosmos.QueryParameter{{Name: "@kind", Value: appendKind}}}
	// Restart after deletion: reusing a continuation over a changing result set
	// can skip payloads. Empty intermediate pages still require their continuation.
	for {
		pager := s.container.NewQueryItemsPager("SELECT VALUE c.id FROM c WHERE c.kind = @kind", partition, options)
		var items [][]byte
		for pager.More() {
			response, err := pager.NextPage(ctx)
			if err != nil {
				return fmt.Errorf("cosmosdb: clear closed payloads: %w", err)
			}
			items = response.Items
			if len(items) > 0 {
				break
			}
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			var itemID string
			if err := jsonv2.Unmarshal(item, &itemID); err != nil {
				return fmt.Errorf("cosmosdb: decode closed payload ID: %w", err)
			}
			if !strings.HasPrefix(itemID, appendDocumentPrefix) || len(itemID) == len(appendDocumentPrefix) {
				return errors.New("cosmosdb: invalid closed payload ID")
			}
			if _, err := s.container.DeleteItem(ctx, partition, itemID, strongItemOptions()); err != nil && responseStatus(err) != http.StatusNotFound {
				return fmt.Errorf("cosmosdb: delete closed payload: %w", err)
			}
		}
	}
}

func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	consistency := requiredConsistency
	options := &azcosmos.QueryOptions{ConsistencyLevel: &consistency, QueryParameters: []azcosmos.QueryParameter{{Name: "@kind", Value: conversationKind}}}
	pager := s.container.NewQueryItemsPager("SELECT VALUE c.conversation_id FROM c WHERE c.kind = @kind AND c.closed = false", azcosmos.NewPartitionKey(), options)
	ids := []history.ConversationID{}
	for pager.More() {
		response, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cosmosdb: list conversations: %w", err)
		}
		for _, item := range response.Items {
			var value string
			if err := jsonv2.Unmarshal(item, &value); err != nil {
				return nil, fmt.Errorf("cosmosdb: decode conversation ID: %w", err)
			}
			id, err := history.NewConversationID(value)
			if err != nil {
				return nil, fmt.Errorf("cosmosdb: invalid stored conversation ID: %w", err)
			}
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
