package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

var (
	ErrInvalidAppendID    = errors.New("history: invalid append ID")
	ErrInvalidBatch       = errors.New("history: invalid append batch")
	ErrInvalidReceipt     = errors.New("history: invalid append receipt")
	ErrAppendConflict     = errors.New("history: append ID already names different messages")
	ErrConversationClosed = errors.New("history: conversation is permanently closed")
)

// AppendID names one immutable append within a conversation. A retry must use
// the same ID and the same complete batch, including after acknowledgment loss.
type AppendID string

func NewAppendID(value string) (AppendID, error) {
	id := AppendID(value)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (a AppendID) String() string { return string(a) }

func (a AppendID) Validate() error {
	if err := validateIdentifier(string(a)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAppendID, err)
	}
	return nil
}

// Batch freezes the complete append before its first write. Canonical encoding
// and its digest come from this owner, so adapters cannot disagree about whether
// a repeated identity carries the same content. Object key order and insignificant
// JSON whitespace do not affect identity; string values and number spellings do.
// Tool-call arguments are strings and retain the model's exact text.
type Batch struct {
	conversationID ConversationID
	id             AppendID
	messages       historyMessages
	encoded        json.RawMessage
	digest         string
}

func NewBatch(conversationID ConversationID, id AppendID, messages ...chat.Message) (Batch, error) {
	if err := conversationID.Validate(); err != nil {
		return Batch{}, fmt.Errorf("%w: %w", ErrInvalidBatch, err)
	}
	if err := id.Validate(); err != nil {
		return Batch{}, fmt.Errorf("%w: %w", ErrInvalidBatch, err)
	}
	if len(messages) == 0 {
		return Batch{}, fmt.Errorf("%w: messages must not be empty", ErrInvalidBatch)
	}
	for index := range messages {
		if err := messages[index].Validate(); err != nil {
			return Batch{}, fmt.Errorf("%w: messages[%d]: %w", ErrInvalidBatch, index, err)
		}
	}
	encoded, err := jsonv2.Marshal(messages, jsonv2.Deterministic(true))
	if err != nil {
		return Batch{}, fmt.Errorf("%w: encode messages: %w", ErrInvalidBatch, err)
	}
	canonical := jsontext.Value(encoded)
	if err := canonical.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return Batch{}, fmt.Errorf("%w: canonical messages: %w", ErrInvalidBatch, err)
	}
	encoded = canonical
	var snapshot historyMessages
	if err := jsonv2.Unmarshal(encoded, &snapshot); err != nil {
		return Batch{}, fmt.Errorf("%w: decode frozen messages: %w", ErrInvalidBatch, err)
	}
	digest := sha256.Sum256(encoded)
	return Batch{conversationID: conversationID, id: id, messages: snapshot, encoded: encoded, digest: hex.EncodeToString(digest[:])}, nil
}

func (b Batch) ConversationID() ConversationID   { return b.conversationID }
func (b Batch) ID() AppendID                     { return b.id }
func (b Batch) Messages() []chat.Message         { return b.messages.clone() }
func (b Batch) EncodedMessages() json.RawMessage { return bytes.Clone(b.encoded) }
func (b Batch) Digest() string                   { return b.digest }
func (b Batch) Len() int                         { return len(b.messages) }

func (b Batch) Validate() error {
	if len(b.encoded) == 0 {
		return ErrInvalidBatch
	}
	return nil
}

func (b Batch) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(batchWire{ConversationID: b.conversationID, ID: b.id, Messages: b.encoded}, jsonv2.Deterministic(true))
}

func (b *Batch) UnmarshalJSON(data []byte) error {
	if b == nil {
		return ErrInvalidBatch
	}
	var wire batchWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode batch: %w", ErrInvalidBatch, err)
	}
	var messages []chat.Message
	if err := jsonv2.Unmarshal(wire.Messages, &messages, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode messages: %w", ErrInvalidBatch, err)
	}
	batch, err := NewBatch(wire.ConversationID, wire.ID, messages...)
	if err != nil {
		return err
	}
	*b = batch
	return nil
}

type batchWire struct {
	ConversationID ConversationID  `json:"conversation_id"`
	ID             AppendID        `json:"id"`
	Messages       json.RawMessage `json:"messages"`
}

// Receipt acknowledges the entire named batch. A zero receipt proves nothing
// about a failed attempt; submitting the same Batch safely resolves that doubt.
// Stores return a receipt only after their atomic append is acknowledged.
type Receipt struct {
	ConversationID ConversationID
	ID             AppendID
	Digest         string
}

func (r Receipt) ValidateFor(batch Batch) error {
	if batch.Validate() != nil || r.ConversationID != batch.ConversationID() || r.ID != batch.ID() || r.Digest != batch.Digest() {
		return ErrInvalidReceipt
	}
	return nil
}
