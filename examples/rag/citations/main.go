// Command citations follows named source bytes through indexing and retrieval
// to the exact evidence selected for an answer. Run go run ./rag/citations
// from the examples module. Both model bindings are deterministic and offline.
package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/inmemory"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/text"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
	"github.com/Tangerg/scope/rag/index"
	"github.com/Tangerg/scope/rag/vector"
)

const (
	policySource           = "manual/returns"
	passageSeparator       = "\n\n"
	policy                 = "Return unopened purchases within 30 days."
	question               = "When can I return an unopened purchase?"
	documentEmbeddingModel = "passages"
	queryEmbeddingModel    = "questions"
	embeddingDimensions    = 2
	searchResultsPerStore  = 2
	contextTokenBudget     = 60
)

var policyFile = policy + "\n\n" + strings.Repeat("Oversized historical return appendix. ", 100)

// This two-coordinate offline fixture counts actual calls at each purpose
// boundary. It is a demonstration of the contract, not an evaluated ranker.
type encoderCalls struct{ documents, queries int }

func (e *encoderCalls) encode(ctx context.Context, r *embedding.Request) (*embedding.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch r.Options.Model {
	case documentEmbeddingModel:
		e.documents++
	case queryEmbeddingModel:
		e.queries++
	default:
		return nil, fmt.Errorf("unexpected encoding purpose %q", r.Options.Model)
	}
	out := make([]*embedding.Output, len(r.Texts))
	for i, s := range r.Texts {
		v := []float64{0, 1}
		if strings.Contains(strings.ToLower(s), "return") {
			v = []float64{1, 0}
		}
		out[i] = &embedding.Output{Embedding: v}
	}
	return embedding.NewResponse(out, nil)
}

type wordCounter struct{}

func (wordCounter) CountText(ctx context.Context, s string) (int, error) {
	return len(strings.Fields(s)), ctx.Err()
}

func ingest(ctx context.Context, encoder *vector.Encoder, source, raw string) (*inmemory.Store, error) {
	reader, err := text.NewReader(strings.NewReader(raw), text.ReaderConfig{SourceName: source})
	if err != nil {
		return nil, err
	}
	docs, err := reader.Read(ctx)
	if err != nil {
		return nil, err
	}
	ids := etl.NewSHA256IDGenerator(nil)
	assign, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: ids})
	if err != nil {
		return nil, err
	}
	docs, err = assign.Assign(ctx, docs)
	if err != nil {
		return nil, err
	}
	passages, err := etl.NewSeparatorSplitter(etl.SeparatorSplitterConfig{Separator: passageSeparator})
	if err != nil {
		return nil, err
	}
	splitter, err := etl.NewSplitter(etl.SplitterConfig{TextSplitter: passages, IDGenerator: ids})
	if err != nil {
		return nil, err
	}
	chunks, err := splitter.Split(ctx, docs)
	if err != nil {
		return nil, err
	}
	store, err := inmemory.NewStore(ctx, inmemory.StoreConfig{Space: encoder.Space(), Dimensions: embeddingDimensions})
	if err != nil {
		return nil, err
	}
	writer, err := index.NewWriter(index.WriterConfig{Encoder: encoder, Destination: store})
	if err != nil {
		return nil, err
	}
	if err = writer.Write(ctx, chunks); err != nil {
		return nil, err
	}
	return store, nil
}

type report struct {
	answer, marker string
	cited          *document.Document
	input          string
	ranked         int
	citations      rag.Citations
	calls          encoderCalls
}

func run(ctx context.Context) (report, error) {
	var result report
	model := embedding.ModelFunc(result.calls.encode)
	encoder, err := vector.NewEncoder(vector.EncoderConfig{Space: "offline/keyword-v1", Documents: embeddingclient.Config{Model: model, Options: embedding.Options{Model: documentEmbeddingModel}}, Queries: embeddingclient.Config{Model: model, Options: embedding.Options{Model: queryEmbeddingModel}}})
	if err != nil {
		return result, err
	}
	// The Host retains originals independently of either vector index.
	originals := map[string]string{policySource: policyFile, "support/receipts": "Find your original receipt in order history."}
	primary, err := ingest(ctx, encoder, policySource, originals[policySource])
	if err != nil {
		return result, err
	}
	notes, err := ingest(ctx, encoder, "support/receipts", originals["support/receipts"])
	if err != nil {
		return result, err
	}

	// This host prefers current policy to support notes. It preserves the native
	// order within each source instead of comparing scores between distinct lists.
	retrieve := rag.RetrieverFunc(func(ctx context.Context, q rag.Query) (rag.Candidates, error) {
		coordinates, encodeErr := encoder.EncodeQuery(ctx, q.Text(), vectorstore.SearchOptions{TopK: searchResultsPerStore})
		if encodeErr != nil {
			return nil, encodeErr
		}
		var candidates rag.Candidates
		for _, store := range []*inmemory.Store{primary, notes} {
			res, queryErr := store.Query(ctx, coordinates)
			if queryErr != nil {
				return nil, queryErr
			}
			for _, row := range res.Results {
				candidates = append(candidates, rag.Candidate{Document: row.Document, Score: rag.Score(row.Score.Float64())})
			}
		}
		return candidates, nil
	})
	augmenter, err := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{MaxContextTokens: contextTokenBudget, TokenCounter: wordCounter{}})
	if err != nil {
		return result, err
	}
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: retrieve, Augmenter: augmenter})
	if err != nil {
		return result, err
	}
	req, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart(question)))
	if err != nil {
		return result, err
	}
	prepared, err := preparer.Prepare(ctx, req)
	if err != nil {
		return result, err
	}

	// A real model is injected at this same boundary. The deterministic answer
	// fixture uses only the supplied prompt and fails if its evidence is absent.
	response, err := prepared.Call(ctx, chat.ModelFunc(func(_ context.Context, r *chat.Request) (*chat.Response, error) {
		result.input = r.Messages[0].Text()
		_, body, ok := strings.Cut(result.input, "\n\n---------------------\n")
		if !ok {
			return nil, errors.New("model input has no evidence block")
		}
		block, tail, ok := strings.Cut(body, "\n---------------------\n\nQuery: ")
		if !ok || !strings.HasPrefix(tail, question+"\n") {
			return nil, errors.New("model input lost its question")
		}
		var rows []struct {
			Citation string `json:"citation"`
			Content  string `json:"content"`
		}
		if parseErr := jsonv2.Unmarshal([]byte(block), &rows); parseErr != nil {
			return nil, parseErr
		}
		var answer string
		for _, row := range rows {
			if strings.Contains(row.Content, "purchases within") {
				answer = row.Content + " " + row.Citation
				break
			}
		}
		if answer == "" {
			return nil, errors.New("no admitted passage answers the question")
		}
		msg := chat.NewAssistantMessage(chat.NewTextPart(answer))
		return chat.NewResponse(&chat.Output{Message: &msg, FinishReason: chat.FinishReasonStop}, nil)
	}))
	if err != nil {
		return result, err
	}
	result.answer = response.Text()
	_, marker, ok := strings.Cut(result.answer, " [")
	if !ok {
		return result, errors.New("answer has no citation")
	}
	result.marker = "[" + marker
	evidence := prepared.Evidence()
	result.ranked = len(evidence.Candidates)
	result.citations = evidence.Citations
	result.cited, err = result.citations.Resolve(result.marker)
	if err != nil {
		return result, err
	}
	p := result.cited.Provenance
	raw, available := originals[p.Source]
	if !available || document.NewProvenance(p.Source, []byte(raw)).Revision != p.Revision {
		return result, errors.New("original revision is unavailable")
	}
	return result, nil
}

func main() {
	r, err := run(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	p := r.cited.Provenance
	chunk := p.Chunks[len(p.Chunks)-1]
	fmt.Printf("%s\n%s → %s @ %s; chunk %d/%d\n", r.answer, r.marker, p.Source, p.Revision, chunk.Index+1, chunk.Total)
	fmt.Printf("Embedding calls: %d document batches, %d query for two indexes. Evidence: %d/%d passages admitted.\n", r.calls.documents, r.calls.queries, r.citations.Len(), r.ranked)
}
