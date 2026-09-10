package rag_test

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
)

// stubRetriever returns a fixed document set; used to exercise the
// middleware without a real vector store.
type stubRetriever struct {
	docs rag.Candidates
}

func (s *stubRetriever) Retrieve(_ context.Context, _ rag.Query) (rag.Candidates, error) {
	return s.docs, nil
}

// echoChatModel mirrors the user's last message back. It implements both
// target chat capabilities so call and stream middleware share one fixture.
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

func TestNewMiddlewareRejectsInvalidConfig(t *testing.T) {
	if _, err := rag.NewMiddleware(rag.MiddlewareConfig{}); err == nil {
		t.Fatal("missing retrievers must error")
	}
	var typedNilRetriever *stubRetriever
	if _, err := rag.NewMiddleware(rag.MiddlewareConfig{Retriever: typedNilRetriever}); err == nil {
		t.Fatal("typed nil retriever must error")
	}
	if _, err := rag.NewMiddleware(rag.MiddlewareConfig{Retriever: &stubRetriever{}}); !errors.Is(err, rag.ErrNilAugmenter) {
		t.Fatalf("missing augmenter error = %v", err)
	}
}

func TestMiddlewarePreservesMissingCapabilities(t *testing.T) {
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
		Retriever: &stubRetriever{}, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if middleware.Call(nil) != nil {
		t.Fatal("call middleware synthesized a model capability")
	}
	if middleware.Stream(nil) != nil {
		t.Fatal("stream middleware synthesized a streaming capability")
	}
}

func TestMiddlewareAugmentsRequestAndAttachesDocs(t *testing.T) {
	doc, _ := document.NewDocument("retrieved info", nil)
	retriever := &stubRetriever{docs: rag.Candidates{candidate(doc)}}
	aug, _ := rag.NewContextualAugmenter(rag.ContextualAugmenterConfig{})
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
		Retriever: retriever,
		Augmenter: aug,
	})
	if err != nil {
		t.Fatal(err)
	}

	model := &echoChatModel{}
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("what is RAG?")))
	response, err := middleware.Call(model).Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(model.captured, "retrieved info") {
		t.Fatalf("augmented user message did not embed retrieved doc: %q", model.captured)
	}
	docs, ok, err := rag.CandidatesFromMetadata(response.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("retrieved candidates not attached to response")
	}
	if len(docs) != 1 {
		t.Fatalf("attached docs len = %d, want 1", len(docs))
	}
	citations, found, err := rag.CitationsFromMetadata(response.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(citations) != 1 || citations[0].Marker() != "[1]" || citations[0].Candidate.Document.Text != doc.Text {
		t.Fatalf("citations = %#v, present %v", citations, found)
	}
}

func TestMiddlewarePreservesChatExtensionsAndExposesTypedHistory(t *testing.T) {
	var capturedHistory []chat.Message
	retriever := rag.RetrieverFunc(func(_ context.Context, query rag.Query) (rag.Candidates, error) {
		var err error
		capturedHistory, _, err = query.Value(rag.HistoryValueKey())
		return nil, err
	})
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
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
	if _, callErr := middleware.Call(model).Call(t.Context(), request); callErr != nil {
		t.Fatal(callErr)
	}
	if downstreamTenant != "acme" {
		t.Fatalf("downstream request extension = %q, want acme", downstreamTenant)
	}
	if len(capturedHistory) != 3 || capturedHistory[0].Text() != "system" || capturedHistory[2].Text() != "first answer" {
		t.Fatalf("chat history = %#v, want messages before the active user turn", capturedHistory)
	}
}

func TestMiddlewareStreamAugmentsOnceAndAttachesDocs(t *testing.T) {
	doc, _ := document.NewDocument("streamed context", nil)
	retriever := &countingRetriever{docs: rag.Candidates{candidate(doc)}}
	aug, _ := rag.NewContextualAugmenter(rag.ContextualAugmenterConfig{})
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{Retriever: retriever, Augmenter: aug})
	if err != nil {
		t.Fatal(err)
	}

	model := &echoChatModel{}
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	var chunks int
	for response, streamErr := range middleware.Stream(model).Stream(t.Context(), request) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		chunks++
		if _, ok, decodeErr := rag.CandidatesFromMetadata(response.Metadata); decodeErr != nil || !ok {
			t.Fatalf("document extension = present %v, error %v", ok, decodeErr)
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

func TestMiddlewarePropagatesRetrieverError(t *testing.T) {
	want := errors.New("boom")
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
		Retriever: &errorRetriever{err: want}, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}

	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hi")))
	_, err = middleware.Call(&echoChatModel{}).Call(t.Context(), request)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func TestMiddlewareRejectsInvalidAugmentation(t *testing.T) {
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
		Retriever: &stubRetriever{},
		Augmenter: rag.AugmenterFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Augmentation, error) {
			return rag.Augmentation{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		called = true
		return textResponse("unexpected"), nil
	})
	request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))

	if _, err := middleware.Call(model).Call(t.Context(), request); !errors.Is(err, rag.ErrInvalidAugmentation) {
		t.Fatalf("invalid augmentation error = %v", err)
	}
	if called {
		t.Fatal("model called with an invalid augmentation")
	}
}

func TestMiddlewarePreservesPartialModelResponse(t *testing.T) {
	doc, _ := document.NewDocument("retrieved info", nil)
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
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

	response, err := middleware.Call(model).Call(t.Context(), request)
	if response != partial || !errors.Is(err, wantErr) {
		t.Fatalf("response/error = %p/%v, want %p/%v", response, err, partial, wantErr)
	}
	if _, found, decodeErr := rag.CandidatesFromMetadata(response.Metadata); decodeErr != nil || !found {
		t.Fatalf("partial response document extension = present %v, error %v", found, decodeErr)
	}
}

func TestMiddlewareRequiresActiveUserTurn(t *testing.T) {
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
		Retriever: &stubRetriever{}, Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewAssistantMessage(chat.NewTextPart("already answered")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := middleware.Call(&echoChatModel{}).Call(t.Context(), request); !errors.Is(err, rag.ErrNoFinalUserMessage) {
		t.Fatalf("final assistant message error = %v", err)
	}
}

type errorRetriever struct {
	err error
}

func (e *errorRetriever) Retrieve(_ context.Context, _ rag.Query) (rag.Candidates, error) {
	return nil, e.err
}

func TestMiddlewareDoesNotMutateCallerMessages(t *testing.T) {
	doc, _ := document.NewDocument("retrieved info", nil)
	retriever := &stubRetriever{docs: rag.Candidates{candidate(doc)}}
	aug, _ := rag.NewContextualAugmenter(rag.ContextualAugmenterConfig{})
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{Retriever: retriever, Augmenter: aug})
	if err != nil {
		t.Fatal(err)
	}

	model := &echoChatModel{}
	userMessage := chat.NewUserMessage(chat.NewTextPart("what is RAG?"))
	request, _ := chat.NewRequest(userMessage)
	if _, err := middleware.Call(model).Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(model.captured, "retrieved info") {
		t.Fatalf("model did not see augmented text: %q", model.captured)
	}
	if got := request.Messages[0].Text(); got != "what is RAG?" {
		t.Fatalf("caller message was mutated: %q", got)
	}
}

func TestMiddlewarePreservesActiveUserPartOrder(t *testing.T) {
	first, err := media.NewURI("image/png", "https://example.com/first.png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := media.NewURI("image/png", "https://example.com/second.png")
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := rag.NewMiddleware(rag.MiddlewareConfig{
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
	if _, err := middleware.Call(model).Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages[0].Parts) != 4 {
		t.Fatalf("caller request was mutated: %#v", request.Messages[0].Parts)
	}
}
