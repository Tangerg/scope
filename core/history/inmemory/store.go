package inmemory

import (
	"context"
	"slices"
	"sync"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store holds messages, append receipts, and closed identities under one lock.
// Its zero value is usable. All records are lost when the process exits.
type Store struct {
	mu            sync.RWMutex
	conversations map[history.ConversationID]*conversation
}

type conversation struct {
	messages []chat.Message
	appends  map[history.AppendID]string
	closed   bool
}

func (s *Store) Append(ctx context.Context, batch history.Batch) (history.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return history.Receipt{}, err
	}
	if err := batch.Validate(); err != nil {
		return history.Receipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return history.Receipt{}, err
	}
	if s.conversations == nil {
		s.conversations = make(map[history.ConversationID]*conversation)
	}
	id := batch.ConversationID()
	stored := s.conversations[id]
	if stored == nil {
		stored = &conversation{appends: make(map[history.AppendID]string)}
		s.conversations[id] = stored
	}
	if stored.closed {
		return history.Receipt{}, history.ErrConversationClosed
	}
	digest, found := stored.appends[batch.ID()]
	if found && digest != batch.Digest() {
		return history.Receipt{}, history.ErrAppendConflict
	}
	if !found {
		stored.messages = append(stored.messages, batch.Messages()...)
		stored.appends[batch.ID()] = batch.Digest()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stored := s.conversations[conversationID]
	if stored == nil || stored.closed {
		return []chat.Message{}, nil
	}
	messages := make([]chat.Message, len(stored.messages))
	for index := range stored.messages {
		messages[index] = stored.messages[index].Clone()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.conversations == nil {
		s.conversations = make(map[history.ConversationID]*conversation)
	}
	stored := s.conversations[conversationID]
	if stored == nil {
		stored = &conversation{}
		s.conversations[conversationID] = stored
	}
	stored.messages = nil
	stored.appends = nil
	stored.closed = true
	return nil
}

func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := make([]history.ConversationID, 0, len(s.conversations))
	for id, stored := range s.conversations {
		if !stored.closed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}
