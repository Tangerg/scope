package index_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/tokenizer"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/rag/index"
	"github.com/Tangerg/scope/rag/vector"
)

type modelCalls struct {
	texts  []string
	models []string
	failAt string
}

func (m *modelCalls) Call(_ context.Context, req *embedding.Request) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, len(req.Texts))
	for i, s := range req.Texts {
		m.texts = append(m.texts, s)
		m.models = append(m.models, req.Options.Model)
		if s == m.failAt {
			return nil, errors.New("model unavailable")
		}
		outputs[i] = &embedding.Output{Embedding: []float64{float64(len(s)), 1}}
	}
	return embedding.NewResponse(outputs, nil)
}

type counter struct{ texts []string }

func (c *counter) CountText(_ context.Context, s string) (int, error) {
	c.texts = append(c.texts, s)
	return len(s), nil
}

type sink struct {
	space    vectorstore.SpaceID
	received *vectorstore.Batch
}

func (s *sink) CheckDocuments(_ context.Context, space vectorstore.SpaceID, _ []*document.Document) error {
	if space != s.space {
		return vectorstore.ErrIncompatibleSpace
	}
	return nil
}

func (s *sink) Upsert(_ context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	s.received = batch
	return nil
}

func sourced(id, text string) *document.Document {
	return &document.Document{ID: id, Text: text, Provenance: document.NewProvenance(id, []byte(text))}
}

func TestPreparationAndEncoderPurpose(t *testing.T) {
	ctx := t.Context()
	m := &modelCalls{}
	const space vectorstore.SpaceID = "paired-revision"
	e, err := vector.NewEncoder(vector.EncoderConfig{Space: space,
		Documents: embeddingclient.Config{Model: m, Options: embedding.Options{Model: "document-model"}},
		Queries:   embeddingclient.Config{Model: m, Options: embedding.Options{Model: "query-model"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &counter{}
	out := &sink{space: space}
	calls := 0
	writer, err := index.NewWriter(index.WriterConfig{Destination: out, Encoder: e, Batching: tokenizer.BatchConfig{Counter: c, MaxTokens: 6},
		Formatter: document.FormatterFunc(func(d *document.Document) (string, error) {
			calls++
			d.ID = "temporary"
			d.Provenance.Revision = "formatter-local"
			return "# " + d.Text, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	docs := []*document.Document{sourced("a", "one"), sourced("b", "two")}
	anonymous := &document.Document{ID: "later", Text: "unverified"}
	if writeErr := writer.Write(ctx, []*document.Document{docs[0], anonymous}); !errors.Is(writeErr, document.ErrUnversionedSource) || calls != 0 || len(m.texts) != 0 || out.received != nil {
		t.Fatalf("missing revision spent model work or wrote an earlier document: %v", writeErr)
	}
	if writeErr := writer.Write(ctx, docs); writeErr != nil {
		t.Fatal(writeErr)
	}
	if !slices.Equal(c.texts, []string{"# one", "# two"}) || !slices.Equal(m.texts, c.texts) || calls != 2 {
		t.Fatalf("format=%d, count=%q, encode=%q", calls, c.texts, m.texts)
	}
	if out.received == nil || out.received.Records[0].Document.ID != "a" || out.received.Records[0].Document.Provenance.Revision != docs[0].Provenance.Revision || out.received.Records[0].Document.Text != "one" || out.received.Records[0].Document == docs[0] {
		t.Fatal("index did not preserve an independent snapshot of the original content")
	}
	q, err := e.EncodeQuery(ctx, "where?", vectorstore.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.models, []string{"document-model", "document-model", "query-model"}) {
		t.Fatalf("incorrect purpose bindings: %q", m.models)
	}
	if err := q.ValidateFor(space); err != nil {
		t.Fatal(err)
	}
	if err := q.ValidateFor("other-space"); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatalf("same width from another space accepted: %v", err)
	}
	if err := q.ValidateFor(space); err != nil {
		t.Fatal(err)
	}
	if len(m.models) != 3 {
		t.Fatalf("precomputed vector reuse invoked model: %q", m.models)
	}

	out.received = nil
	m.failAt = "# two"
	if err := writer.Write(ctx, docs); err == nil || out.received != nil {
		t.Fatalf("later embedding failure published a prefix: %v", err)
	}
	m.texts = nil
	out.space = "different"
	if err := writer.Write(ctx, docs); !errors.Is(err, vectorstore.ErrIncompatibleSpace) || len(m.texts) != 0 {
		t.Fatalf("wrong space reached paid model: calls=%q err=%v", m.texts, err)
	}
}

func TestRejectedPreparationDoesNotSpendOrPublish(t *testing.T) {
	model := new(modelCalls)
	const space vectorstore.SpaceID = "test"
	encoder, err := vector.NewEncoder(vector.EncoderConfig{Space: space, Documents: embeddingclient.Config{Model: model}, Queries: embeddingclient.Config{Model: model}})
	if err != nil {
		t.Fatal(err)
	}
	target := &sink{space: space}
	for _, cfg := range []index.WriterConfig{{Encoder: encoder}, {Destination: target}, {Encoder: encoder, Destination: target, Batching: tokenizer.BatchConfig{MaxTokens: 3}}, {Encoder: encoder, Destination: target, Batching: tokenizer.BatchConfig{Counter: new(counter)}}, {Encoder: encoder, Destination: target, Batching: tokenizer.BatchConfig{MaxItems: -1}}} {
		if w, configErr := index.NewWriter(cfg); configErr == nil || w != nil {
			t.Fatalf("accepted invalid sizing configuration: %v", cfg)
		}
	}
	input := []*document.Document{sourced("one", "first"), sourced("two", "second")}
	failure := errors.New("missing required field")
	for name, configure := range map[string]func(*index.WriterConfig){
		"formatter": func(c *index.WriterConfig) {
			c.Formatter = document.FormatterFunc(func(d *document.Document) (string, error) {
				if d.ID == "two" {
					return "", failure
				}
				return d.Text, nil
			})
		},
		"oversize": func(c *index.WriterConfig) { c.Batching = tokenizer.BatchConfig{Counter: new(counter), MaxTokens: 1} },
		"empty model input": func(c *index.WriterConfig) {
			c.Formatter = document.FormatterFunc(func(*document.Document) (string, error) { return "", nil })
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := index.WriterConfig{Encoder: encoder, Destination: target}
			configure(&cfg)
			writer, buildErr := index.NewWriter(cfg)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			writeErr := writer.Write(t.Context(), input)
			if writeErr == nil || name == "formatter" && !errors.Is(writeErr, failure) || len(model.texts) != 0 || target.received != nil {
				t.Fatalf("rejected preparation spent work: %v", writeErr)
			}
		})
	}
	writer, err := index.NewWriter(index.WriterConfig{Encoder: encoder, Destination: target})
	if err != nil {
		t.Fatal(err)
	}
	input[1].Metadata = metadata.Map{document.StoredProvenanceKey: []byte("null")}
	if err = writer.Write(t.Context(), input); !errors.Is(err, vectorstore.ErrInvalidDocument) || len(model.texts) != 0 || target.received != nil {
		t.Fatalf("a forged storage field reached paid preparation or native storage: %v", err)
	}
}

func TestUnboundEncoderCannotReachEitherModel(t *testing.T) {
	model := new(modelCalls)
	for _, cfg := range []vector.EncoderConfig{{Space: "", Documents: embeddingclient.Config{Model: model}, Queries: embeddingclient.Config{Model: model}}, {Space: "valid", Documents: embeddingclient.Config{Model: model}}, {Space: "valid", Queries: embeddingclient.Config{Model: model}}} {
		if e, err := vector.NewEncoder(cfg); err == nil || e != nil {
			t.Fatal("accepted an incomplete asymmetric space")
		}
	}
	if len(model.texts) != 0 {
		t.Fatal("invalid space reached the model")
	}
}
