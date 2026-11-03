package cosmosdb

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/Tangerg/scope/core/history"
)

type documentKind string

const (
	conversationKind           documentKind = "conversation"
	appendKind                 documentKind = "append"
	appendDocumentPrefix                    = string(appendKind) + ":"
	permanentTTL               int32        = -1
	sequenceWidth                           = 19
	maximumItemIDBytes                      = 1023
	maximumAppendDocumentBytes              = 2 << 20
)

// This gate never expires: expiration or deletion would make a closed identity
// writable again. The decimal sequence avoids Cosmos JSON number precision loss.
type conversationDocument struct {
	ID             string       `json:"id"`
	Kind           documentKind `json:"kind"`
	ConversationID string       `json:"conversation_id"`
	Closed         *bool        `json:"closed"`
	LastSequence   string       `json:"last_sequence"`
	TTL            int32        `json:"ttl"`
}

func newConversationDocument(id history.ConversationID) conversationDocument {
	closed := false
	return conversationDocument{ID: string(conversationKind), Kind: conversationKind, ConversationID: id.String(), Closed: &closed, LastSequence: formatSequence(0), TTL: permanentTTL}
}

func (c conversationDocument) validate(id history.ConversationID) error {
	if c.ID != string(conversationKind) || c.Kind != conversationKind || c.ConversationID != id.String() || c.Closed == nil || c.TTL != permanentTTL || !validSequence(c.LastSequence) {
		return errors.New("cosmosdb: invalid conversation document")
	}
	return nil
}

func (c conversationDocument) closed() bool { return *c.Closed }

func (c conversationDocument) close() conversationDocument {
	closed := true
	c.Closed = &closed
	return c
}

func (c conversationDocument) advance() (conversationDocument, error) {
	if c.Closed == nil {
		return c, errors.New("cosmosdb: missing conversation closure state")
	}
	if c.closed() {
		return c, history.ErrConversationClosed
	}
	sequence, err := strconv.ParseInt(c.LastSequence, 10, 64)
	if err != nil || sequence < 0 || sequence == math.MaxInt64 {
		return c, errors.New("cosmosdb: conversation sequence exhausted or invalid")
	}
	c.LastSequence = formatSequence(sequence + 1)
	return c, nil
}

// Receipt identity and payload occupy the same item and transaction. Clear may
// remove this item only after the permanent gate has closed the conversation.
type appendDocument struct {
	ID             string       `json:"id"`
	Kind           documentKind `json:"kind"`
	ConversationID string       `json:"conversation_id"`
	AppendID       string       `json:"append_id"`
	Digest         string       `json:"digest"`
	Sequence       string       `json:"seq"`
	Messages       string       `json:"messages"`
	TTL            int32        `json:"ttl"`
}

func (a appendDocument) validate(conversationID history.ConversationID) error {
	id, err := history.NewAppendID(a.AppendID)
	if err != nil {
		return fmt.Errorf("cosmosdb: invalid stored append identity: %w", err)
	}
	itemID, err := appendDocumentID(id)
	if err != nil {
		return err
	}
	if a.ID != itemID || a.Kind != appendKind || a.ConversationID != conversationID.String() || a.Digest == "" || a.TTL != permanentTTL || !validSequence(a.Sequence) || a.Sequence == formatSequence(0) || a.Messages == "" {
		return errors.New("cosmosdb: invalid append document")
	}
	return nil
}

func appendDocumentID(id history.AppendID) (string, error) {
	encoded := appendDocumentPrefix + base64.RawURLEncoding.EncodeToString([]byte(id.String()))
	if len(encoded) > maximumItemIDBytes {
		return "", fmt.Errorf("cosmosdb: append ID exceeds the %d-byte item ID limit after encoding", maximumItemIDBytes)
	}
	return encoded, nil
}

func formatSequence(sequence int64) string { return fmt.Sprintf("%0*d", sequenceWidth, sequence) }

func validSequence(sequence string) bool {
	value, err := strconv.ParseInt(sequence, 10, 64)
	return err == nil && value >= 0 && formatSequence(value) == sequence
}
