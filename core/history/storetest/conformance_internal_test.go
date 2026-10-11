package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
)

type brokenReader struct{}

func (brokenReader) Read(context.Context, history.ConversationID) ([]chat.Message, error) {
	return nil, nil
}

type brokenWriter struct{}

func (brokenWriter) Write(context.Context, history.ConversationID, ...chat.Message) (history.WriteOutcome, error) {
	return history.WriteOutcome{}, errors.New("broken writer ignored its contract")
}

type brokenClearer struct{}

func (brokenClearer) Clear(context.Context, history.ConversationID) error { return nil }

type brokenLister struct{}

func (brokenLister) Conversations(context.Context) ([]history.ConversationID, error) {
	return nil, nil
}

// TestChecksAcceptConformingStoreAndFlagBrokenOne verifies the conformance suite
// itself: every pure check passes a conforming store and flags a non-conforming
// one. Exercising the failure side covers the violation diagnostics the
// happy-path Run against inmemory never reaches.
func TestChecksAcceptConformingStoreAndFlagBrokenOne(t *testing.T) {
	ctx := t.Context()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	good := new(inmemory.Store)

	assertCheck(t, "reader canceled",
		readerRejectsCanceled(good, canceledCtx), readerRejectsCanceled(brokenReader{}, canceledCtx))
	assertCheck(t, "reader invalid conversation",
		readerRejectsInvalidConversation(good, ctx), readerRejectsInvalidConversation(brokenReader{}, ctx))
	assertCheck(t, "writer canceled",
		writerRejectsCanceled(good, canceledCtx), writerRejectsCanceled(brokenWriter{}, canceledCtx))
	assertCheck(t, "writer invalid conversation",
		writerRejectsInvalidConversation(good, ctx), writerRejectsInvalidConversation(brokenWriter{}, ctx))
	assertCheck(t, "writer invalid message",
		writerRejectsInvalidMessage(good, ctx), writerRejectsInvalidMessage(brokenWriter{}, ctx))
	assertCheck(t, "writer empty no-op",
		writerTreatsEmptyMessagesAsNoop(good, ctx), writerTreatsEmptyMessagesAsNoop(brokenWriter{}, ctx))
	assertCheck(t, "clearer canceled",
		clearerRejectsCanceled(good, canceledCtx), clearerRejectsCanceled(brokenClearer{}, canceledCtx))
	assertCheck(t, "clearer invalid conversation",
		clearerRejectsInvalidConversation(good, ctx), clearerRejectsInvalidConversation(brokenClearer{}, ctx))
	assertCheck(t, "lister canceled",
		listerRejectsCanceled(good, canceledCtx), listerRejectsCanceled(brokenLister{}, canceledCtx))

	if capabilityViolation("Reader", true, true) != "" {
		t.Error("capabilityViolation flagged a matching capability")
	}
	if capabilityViolation("Reader", true, false) == "" {
		t.Error("capabilityViolation missed a capability mismatch")
	}
}

func assertCheck(t *testing.T, name, conforming, broken string) {
	t.Helper()
	if conforming != "" {
		t.Errorf("%s: conforming store flagged: %s", name, conforming)
	}
	if broken == "" {
		t.Errorf("%s: broken store not flagged", name)
	}
}
