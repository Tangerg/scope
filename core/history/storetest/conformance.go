// Package storetest provides reusable conformance checks for history stores.
package storetest

import (
	"context"
	"errors"
	"fmt"
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
//
// Each check is a pure function returning a violation message, reported through
// named subtests. The checks are verified independently against conforming and
// non-conforming stores, so the suite's own detection is tested.
func Run(t *testing.T, store any, want Capabilities) {
	t.Helper()
	if store == nil {
		t.Fatal("conformance: store must not be nil")
	}

	got := inspectCapabilities(store)

	runCheck(t, "Reader capability", capabilityViolation("Reader", got.reader != nil, want.Reader))
	runCheck(t, "Writer capability", capabilityViolation("Writer", got.writer != nil, want.Writer))
	runCheck(t, "Clearer capability", capabilityViolation("Clearer", got.clearer != nil, want.Clearer))
	runCheck(t, "Lister capability", capabilityViolation("Lister", got.lister != nil, want.Lister))

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

// runCheck reports one pre-evaluated violation under its own subtest. The check
// ran before this call, so the subtest only surfaces its result. An empty
// message means the store satisfied the check.
func runCheck(t *testing.T, name, violation string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if violation != "" {
			t.Fatal(violation)
		}
	})
}

func runReaderTests(t *testing.T, reader history.Reader, ctx, canceledCtx context.Context) {
	t.Helper()
	runCheck(t, "ReadRejectsCanceledContextBeforeIO", readerRejectsCanceled(reader, canceledCtx))
	runCheck(t, "ReadRejectsInvalidConversationBeforeIO", readerRejectsInvalidConversation(reader, ctx))
}

func readerRejectsCanceled(reader history.Reader, canceledCtx context.Context) string {
	if _, err := reader.Read(canceledCtx, "conversation"); !errors.Is(err, context.Canceled) {
		return fmt.Sprintf("Read(canceled context) error = %v, want %v", err, context.Canceled)
	}
	return ""
}

func readerRejectsInvalidConversation(reader history.Reader, ctx context.Context) string {
	if _, err := reader.Read(ctx, ""); !errors.Is(err, history.ErrInvalidConversationID) {
		return fmt.Sprintf("Read(empty conversation) error = %v, want %v", err, history.ErrInvalidConversationID)
	}
	return ""
}

func runWriterTests(t *testing.T, writer history.Writer, ctx, canceledCtx context.Context) {
	t.Helper()
	runCheck(t, "AppendRejectsCanceledContextBeforeIO", writerRejectsCanceled(writer, canceledCtx))
	runCheck(t, "AppendRejectsInvalidBatchBeforeIO", writerRejectsInvalidBatch(writer, ctx))
}

func writerRejectsCanceled(writer history.Writer, canceledCtx context.Context) string {
	batch, err := history.NewBatch("conformance", "canceled", chat.NewUserMessage(chat.NewTextPart("message")))
	if err != nil {
		return err.Error()
	}
	if receipt, err := writer.Append(canceledCtx, batch); !errors.Is(err, context.Canceled) || receipt != (history.Receipt{}) {
		return fmt.Sprintf("Append(canceled context) receipt = %v, error = %v, want %v", receipt, err, context.Canceled)
	}
	return ""
}

func writerRejectsInvalidBatch(writer history.Writer, ctx context.Context) string {
	if receipt, err := writer.Append(ctx, history.Batch{}); !errors.Is(err, history.ErrInvalidBatch) || receipt != (history.Receipt{}) {
		return fmt.Sprintf("Append(invalid batch) receipt = %v, error = %v, want %v", receipt, err, history.ErrInvalidBatch)
	}
	return ""
}

func runClearerTests(t *testing.T, clearer history.Clearer, ctx, canceledCtx context.Context) {
	t.Helper()
	runCheck(t, "ClearRejectsCanceledContextBeforeIO", clearerRejectsCanceled(clearer, canceledCtx))
	runCheck(t, "ClearRejectsInvalidConversationBeforeIO", clearerRejectsInvalidConversation(clearer, ctx))
}

func clearerRejectsCanceled(clearer history.Clearer, canceledCtx context.Context) string {
	if err := clearer.Clear(canceledCtx, "conversation"); !errors.Is(err, context.Canceled) {
		return fmt.Sprintf("Clear(canceled context) error = %v, want %v", err, context.Canceled)
	}
	return ""
}

func clearerRejectsInvalidConversation(clearer history.Clearer, ctx context.Context) string {
	if err := clearer.Clear(ctx, ""); !errors.Is(err, history.ErrInvalidConversationID) {
		return fmt.Sprintf("Clear(empty conversation) error = %v, want %v", err, history.ErrInvalidConversationID)
	}
	return ""
}

func runListerTests(t *testing.T, lister history.Lister, canceledCtx context.Context) {
	t.Helper()
	runCheck(t, "ConversationsRejectsCanceledContextBeforeIO", listerRejectsCanceled(lister, canceledCtx))
}

func listerRejectsCanceled(lister history.Lister, canceledCtx context.Context) string {
	if _, err := lister.Conversations(canceledCtx); !errors.Is(err, context.Canceled) {
		return fmt.Sprintf("Conversations(canceled context) error = %v, want %v", err, context.Canceled)
	}
	return ""
}

func capabilityViolation(name string, got, want bool) string {
	if got != want {
		return fmt.Sprintf("%s capability = %t, want %t", name, got, want)
	}
	return ""
}
