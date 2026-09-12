// Package storetest provides reusable conformance checks for history stores.
package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

// Capabilities is the exact history interface set a store promises. False
// means the store must not accidentally satisfy that capability.
type Capabilities struct {
	Reader  bool
	Writer  bool
	Clearer bool
	Lister  bool
}

// Run verifies a store's exact capability set and the validation that must
// complete before external I/O. Pass a non-nil zero-value *Store; the calls
// below must not reach provider dependencies.
func Run(t *testing.T, store any, want Capabilities) {
	t.Helper()
	if store == nil {
		t.Fatal("conformance: store must not be nil")
	}

	got := inspectCapabilities(store)

	assertCapability(t, "Reader", got.reader != nil, want.Reader)
	assertCapability(t, "Writer", got.writer != nil, want.Writer)
	assertCapability(t, "Clearer", got.clearer != nil, want.Clearer)
	assertCapability(t, "Lister", got.lister != nil, want.Lister)

	ctx := t.Context()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	if want.Reader && got.reader != nil {
		runReaderTests(t, got.reader, ctx, canceledCtx)
	}
	if want.Writer && got.writer != nil {
		runWriterTests(t, got.writer, ctx, canceledCtx)
	}
	if want.Clearer && got.clearer != nil {
		runClearerTests(t, got.clearer, ctx, canceledCtx)
	}
	if want.Lister && got.lister != nil {
		runListerTests(t, got.lister, canceledCtx)
	}
}

type capabilities struct {
	reader  history.Reader
	writer  history.Writer
	clearer history.Clearer
	lister  history.Lister
}

func inspectCapabilities(store any) capabilities {
	result := capabilities{}
	result.reader, _ = store.(history.Reader)
	result.writer, _ = store.(history.Writer)
	result.clearer, _ = store.(history.Clearer)
	result.lister, _ = store.(history.Lister)
	return result
}

func runReaderTests(t *testing.T, reader history.Reader, ctx, canceledCtx context.Context) {
	t.Helper()
	t.Run("ReadRejectsCanceledContextBeforeIO", func(t *testing.T) {
		if _, err := reader.Read(canceledCtx, "conversation"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Read(canceled context) error = %v, want %v", err, context.Canceled)
		}
	})
	t.Run("ReadRejectsInvalidConversationBeforeIO", func(t *testing.T) {
		if _, err := reader.Read(ctx, ""); !errors.Is(err, history.ErrInvalidConversationID) {
			t.Fatalf("Read(empty conversation) error = %v, want %v", err, history.ErrInvalidConversationID)
		}
	})
}

func runWriterTests(t *testing.T, writer history.Writer, ctx, canceledCtx context.Context) {
	t.Helper()
	t.Run("WriteRejectsCanceledContextBeforeIO", func(t *testing.T) {
		if outcome, err := writer.Write(canceledCtx, "conversation"); !errors.Is(err, context.Canceled) || outcome != (history.WriteOutcome{}) {
			t.Fatalf("Write(canceled context) error = %v, want %v", err, context.Canceled)
		}
	})
	t.Run("WriteRejectsInvalidConversationBeforeIO", func(t *testing.T) {
		if outcome, err := writer.Write(ctx, ""); !errors.Is(err, history.ErrInvalidConversationID) || outcome != (history.WriteOutcome{}) {
			t.Fatalf("Write(empty conversation) error = %v, want %v", err, history.ErrInvalidConversationID)
		}
	})
	t.Run("WriteRejectsInvalidMessageBeforeIO", func(t *testing.T) {
		if outcome, err := writer.Write(ctx, "conversation", chat.Message{}); !errors.Is(err, chat.ErrInvalidMessage) || outcome != (history.WriteOutcome{}) {
			t.Fatalf("Write(invalid message) error = %v, want %v", err, chat.ErrInvalidMessage)
		}
	})
	t.Run("WriteTreatsEmptyMessagesAsNoop", func(t *testing.T) {
		if outcome, err := writer.Write(ctx, "conversation"); err != nil || outcome != (history.WriteOutcome{}) {
			t.Fatalf("Write(empty messages) error = %v, want nil", err)
		}
	})
}

func runClearerTests(t *testing.T, clearer history.Clearer, ctx, canceledCtx context.Context) {
	t.Helper()
	t.Run("ClearRejectsCanceledContextBeforeIO", func(t *testing.T) {
		if err := clearer.Clear(canceledCtx, "conversation"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Clear(canceled context) error = %v, want %v", err, context.Canceled)
		}
	})
	t.Run("ClearRejectsInvalidConversationBeforeIO", func(t *testing.T) {
		if err := clearer.Clear(ctx, ""); !errors.Is(err, history.ErrInvalidConversationID) {
			t.Fatalf("Clear(empty conversation) error = %v, want %v", err, history.ErrInvalidConversationID)
		}
	})
}

func runListerTests(t *testing.T, lister history.Lister, canceledCtx context.Context) {
	t.Helper()
	t.Run("ConversationsRejectsCanceledContextBeforeIO", func(t *testing.T) {
		if _, err := lister.Conversations(canceledCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Conversations(canceled context) error = %v, want %v", err, context.Canceled)
		}
	})
}

func assertCapability(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Errorf("%s capability = %t, want %t", name, got, want)
	}
}
