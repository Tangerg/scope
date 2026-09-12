package chat_test

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

// stubRetriever returns a fixed document set; used to exercise the
// preparer without a real vector store.
type stubRetriever struct {
	docs rag.Candidates
}

func (s *stubRetriever) Retrieve(_ context.Context, _ rag.Query) (rag.Candidates, error) {
	return s.docs, nil
}

// echoChatModel mirrors the user's last message back. It implements both
// target chat capabilities so call and stream preparer share one fixture.
type echoChatModel struct {
	captured string
}

func (e *echoChatModel) capture(req *chat.Request) string {
	for index := len(req.Messages) - 1; index >= 0; index-- {
		if req.Messages[index].Role == chat.RoleUser {
			e.captured = req.Messages[index].Text()
			return e.captured
		}
	}
	return ""
}

func textResponse(text string) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	response, err := chat.NewResponse(&chat.Output{
		Message:      &message,
		FinishReason: chat.FinishReasonStop,
	}, nil)
	if err != nil {
		panic(err)
	}
	return response
}

func (e *echoChatModel) Call(_ context.Context, req *chat.Request) (*chat.Response, error) {
	return textResponse(e.capture(req)), nil
}

func (e *echoChatModel) Stream(_ context.Context, req *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	return func(yield func(*chat.ResponseDelta, error) bool) {
		yield(&chat.ResponseDelta{
			Parts:        []chat.PartDelta{chat.NewTextDelta(e.capture(req))},
			FinishReason: chat.FinishReasonStop,
		}, nil)
	}
}

func TestNewPreparerRejectsInvalidConfig(t *testing.T) {
	if _, err := ragchat.NewPreparer(ragchat.PreparerConfig{}); err == nil {
		t.Fatal("missing retrievers must error")
	}
	var typedNilRetriever *stubRetriever
	if _, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: typedNilRetriever}); err == nil {
		t.Fatal("typed nil retriever must error")
	}
	if _, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: &stubRetriever{}}); !errors.Is(err, rag.ErrNilAugmenter) {
		t.Fatalf("missing augmenter error = %v", err)
	}
}

func TestPreparedRequestRejectsMissingCapabilities(t *testing.T) {
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &stubRetriever{}, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustPrepare(t, preparer, request)
	var typedNil *echoChatModel
	for _, model := range []chat.Model{nil, typedNil} {
		if _, callErr := prepared.Call(t.Context(), model); callErr == nil {
			t.Fatal("missing model accepted")
		}
	}
	for _, streamer := range []chat.Streamer{nil, typedNil} {
		for delta, streamErr := range prepared.Stream(t.Context(), streamer) {
			if delta != nil || streamErr == nil {
				t.Fatalf("missing streamer = %v, %v", delta, streamErr)
			}
		}
	}
	var zero ragchat.PreparedRequest
	if _, callErr := zero.Call(t.Context(), &echoChatModel{}); !errors.Is(callErr, chat.ErrInvalidRequest) {
		t.Fatalf("unprepared call = %v", callErr)
	}
	for delta, streamErr := range zero.Stream(t.Context(), &echoChatModel{}) {
		if delta != nil || !errors.Is(streamErr, chat.ErrInvalidRequest) {
			t.Fatalf("unprepared stream = %v, %v", delta, streamErr)
		}
	}
}

func mustPrepare(t *testing.T, preparer *ragchat.Preparer, request *chat.Request) ragchat.PreparedRequest {
	t.Helper()
	prepared, err := preparer.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestPreparerAugmentsRequestAndOwnsEvidence(t *testing.T) {
	doc, _ := document.NewDocument("retrieved info", nil)
	retriever := &stubRetriever{docs: rag.Candidates{candidate(doc)}}
	aug, _ := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{})
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: retriever,
		Augmenter: aug,
	})
	if err != nil {
		t.Fatal(err)
	}

	model := &echoChatModel{}
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("what is RAG?")))
	prepared := mustPrepare(t, preparer, request)
	response, err := prepared.Call(t.Context(), model)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(model.captured, "retrieved info") {
		t.Fatalf("augmented user message did not embed retrieved doc: %q", model.captured)
	}
	if response.Metadata != nil {
		t.Fatal("retrieval changed model metadata")
	}
	evidence := prepared.Evidence()
	if len(evidence.Candidates) != 1 {
		t.Fatalf("candidates = %#v", evidence.Candidates)
	}
	citations := evidence.Citations
	if len(citations) != 1 || citations[0].Marker() != "[1]" || citations[0].Candidate.Document.Text != doc.Text {
		t.Fatalf("citations = %#v", citations)
	}
	evidence.Candidates[0].Document.Text = "changed"
	evidence.Citations[0].Candidate.Document.Text = "changed"
	again := prepared.Evidence()
	if again.Candidates[0].Document.Text != doc.Text || again.Citations[0].Candidate.Document.Text != doc.Text {
		t.Fatal("evidence snapshot mutated preparation")
	}
}

func TestPreparerPreservesChatExtensionsAndExposesTypedHistory(t *testing.T) {
	var capturedHistory []chat.Message
	retriever := rag.RetrieverFunc(func(_ context.Context, query rag.Query) (rag.Candidates, error) {
		var err error
		capturedHistory, _, err = query.Value(ragchat.HistoryValueKey())
		return nil, err
	})
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: retriever, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := chat.NewRequest(
		chat.NewSystemMessage("system"),
		chat.NewUserMessage(chat.NewTextPart("first question")),
		chat.NewAssistantMessage(chat.NewTextPart("first answer")),
		chat.NewUserMessage(chat.NewTextPart("question")),
	)
	if setExtensionErr := request.Options.Extensions.Set("test/tenant", "acme"); setExtensionErr != nil {
		t.Fatal(setExtensionErr)
	}
	var downstreamTenant string
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		var found bool
		var err error
		downstreamTenant, found, err = request.Options.Extensions.Decode[string]("test/tenant")
		if err != nil || !found {
			return nil, err
		}
		return textResponse("answer"), nil
	})
	if _, callErr := mustPrepare(t, preparer, request).Call(t.Context(), model); callErr != nil {
		t.Fatal(callErr)
	}
	if downstreamTenant != "acme" {
		t.Fatalf("downstream request extension = %q, want acme", downstreamTenant)
	}
	if len(capturedHistory) != 3 || capturedHistory[0].Text() != "system" || capturedHistory[2].Text() != "first answer" {
		t.Fatalf("chat history = %#v, want messages before the active user turn", capturedHistory)
	}
}

func TestPreparerStreamAugmentsOnceAndOwnsEvidence(t *testing.T) {
	doc, _ := document.NewDocument("streamed context", nil)
	retriever := &countingRetriever{docs: rag.Candidates{candidate(doc)}}
	aug, _ := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{})
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: retriever, Augmenter: aug})
	if err != nil {
		t.Fatal(err)
	}

	model := &echoChatModel{}
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	var chunks int
	prepared := mustPrepare(t, preparer, request)
	for response, streamErr := range prepared.Stream(t.Context(), model) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		chunks++
		if response.Metadata != nil {
			t.Fatal("retrieval changed delta metadata")
		}
		if len(prepared.Evidence().Candidates) != 1 {
			t.Fatal("missing evidence")
		}
	}
	if chunks != 1 || retriever.hits != 1 {
		t.Fatalf("chunks = %d, retrievals = %d; want 1, 1", chunks, retriever.hits)
	}
	if !strings.Contains(model.captured, "streamed context") {
		t.Fatalf("stream model did not see augmented text: %q", model.captured)
	}
}

type countingRetriever struct {
	docs rag.Candidates
	hits int
}

func (c *countingRetriever) Retrieve(_ context.Context, _ rag.Query) (rag.Candidates, error) {
	c.hits++
	return c.docs, nil
}

func TestPreparerPropagatesRetrieverError(t *testing.T) {
	want := errors.New("boom")
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &errorRetriever{err: want}, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}

	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hi")))
	_, err = preparer.Prepare(t.Context(), request)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestPreparerRejectsInvalidAugmentation(t *testing.T) {
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &stubRetriever{},
		Augmenter: rag.AugmenterFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Augmentation, error) {
			return rag.Augmentation{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	if _, prepareErr := preparer.Prepare(t.Context(), request); !errors.Is(prepareErr, rag.ErrInvalidAugmentation) {
		t.Fatalf("invalid augmentation error = %v", prepareErr)
	}
}

func TestPreparerPreservesPartialModelResponse(t *testing.T) {
	doc, _ := document.NewDocument("retrieved info", nil)
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &stubRetriever{docs: rag.Candidates{candidate(doc)}},
		Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("partial model failure")
	partial := textResponse("partial")
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return partial, wantErr
	})
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))

	prepared := mustPrepare(t, preparer, request)
	response, err := prepared.Call(t.Context(), model)
	if response != partial || !errors.Is(err, wantErr) {
		t.Fatalf("response/error = %p/%v, want %p/%v", response, err, partial, wantErr)
	}
	if len(prepared.Evidence().Candidates) != 1 {
		t.Fatal("model failure lost retrieval evidence")
	}
}

func TestPreparerRequiresActiveUserTurn(t *testing.T) {
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &stubRetriever{}, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewAssistantMessage(chat.NewTextPart("already answered")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparer.Prepare(t.Context(), request); !errors.Is(err, ragchat.ErrNoFinalUserMessage) {
		t.Fatalf("final assistant message error = %v", err)
	}
}

type errorRetriever struct {
	err error
}

func (e *errorRetriever) Retrieve(_ context.Context, _ rag.Query) (rag.Candidates, error) {
	return nil, e.err
}

func TestPreparerDoesNotMutateCallerMessages(t *testing.T) {
	doc, _ := document.NewDocument("retrieved info", nil)
	retriever := &stubRetriever{docs: rag.Candidates{candidate(doc)}}
	aug, _ := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{})
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: retriever, Augmenter: aug})
	if err != nil {
		t.Fatal(err)
	}

	model := &echoChatModel{}
	userMessage := chat.NewUserMessage(chat.NewTextPart("what is RAG?"))
	request, _ := chat.NewRequest(userMessage)
	if _, err := mustPrepare(t, preparer, request).Call(t.Context(), model); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(model.captured, "retrieved info") {
		t.Fatalf("model did not see augmented text: %q", model.captured)
	}
	if got := request.Messages[0].Text(); got != "what is RAG?" {
		t.Fatalf("caller message was mutated: %q", got)
	}
}

func TestPreparerPreservesActiveUserPartOrder(t *testing.T) {
	first, err := media.NewURI("image/png", "https://example.com/first.png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := media.NewURI("image/png", "https://example.com/second.png")
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &stubRetriever{}, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(
		chat.NewMediaPart(first),
		chat.NewTextPart("first"),
		chat.NewMediaPart(second),
		chat.NewTextPart("second"),
	))
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		parts := request.Messages[len(request.Messages)-1].Parts
		if len(parts) != 4 || parts[0].Kind != chat.PartMedia || parts[1].Kind != chat.PartText || parts[2].Kind != chat.PartMedia || parts[3].Kind != chat.PartText {
			t.Fatalf("active user parts = %#v", parts)
		}
		if parts[1].Text != "first" || parts[3].Text != "second" || parts[0].Media == first || parts[2].Media == second {
			t.Fatalf("active user parts were not independently preserved: %#v", parts)
		}
		return textResponse("answer"), nil
	})
	if _, err := mustPrepare(t, preparer, request).Call(t.Context(), model); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages[0].Parts) != 4 {
		t.Fatalf("caller request was mutated: %#v", request.Messages[0].Parts)
	}
}
