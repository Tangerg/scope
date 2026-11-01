package vector

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Encoding binds the actual provider model and effective per-purpose options;
// asymmetric providers receive their native document or query input type here.
type Encoding struct {
	Model   embedding.Model
	Options embedding.Options
}

type EncoderConfig struct {
	Space     vectorstore.SpaceID
	Documents Encoding
	Queries   Encoding
}

// Encoder binds both halves of a compatible vector space. The Host identifies
// the deployed revisions and preprocessing; neither a model name nor a vector
// width is an adequate substitute for that binding.
type Encoder struct {
	space     vectorstore.SpaceID
	documents Encoding
	queries   Encoding
}

func NewEncoder(config EncoderConfig) (*Encoder, error) {
	if err := config.Space.Validate(); err != nil {
		return nil, err
	}
	for _, binding := range []Encoding{config.Documents, config.Queries} {
		if lo.IsNil(binding.Model) {
			return nil, errors.New("rag/vector: missing embedding model binding")
		}
		if err := binding.Options.Validate(); err != nil {
			return nil, err
		}
	}
	config.Documents.Options = config.Documents.Options.Clone()
	config.Queries.Options = config.Queries.Options.Clone()
	return &Encoder{space: config.Space, documents: config.Documents, queries: config.Queries}, nil
}

func (e *Encoder) Space() vectorstore.SpaceID { return e.space }

func (e *Encoder) EncodeDocuments(ctx context.Context, texts []string) ([][]float64, error) {
	return e.documents.encode(ctx, texts)
}

// EncodeQuery returns coordinates that can be reused across compatible native
// indexes. Lexical text is retained only when the selected search requires it.
func (e *Encoder) EncodeQuery(ctx context.Context, text string, options vectorstore.SearchOptions) (*vectorstore.Query, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	values, err := e.queries.encode(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	q := &vectorstore.Query{Space: e.space, Values: values[0], Options: options}
	if options.EffectiveMode() == vectorstore.SearchModeHybrid {
		q.LexicalText = text
	}
	return q, q.ValidateFor(e.space)
}

func (e Encoding) encode(ctx context.Context, texts []string) ([][]float64, error) {
	if lo.IsNil(e.Model) {
		return nil, errors.New("rag/vector: uninitialized encoder binding")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := embedding.NewRequest(texts)
	if err != nil {
		return nil, err
	}
	req.Options = e.Options.Clone()
	res, err := e.Model.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := res.ValidateFor(req); err != nil {
		return nil, fmt.Errorf("rag/vector: embedding response: %w", err)
	}
	result := make([][]float64, len(res.Outputs))
	for i, output := range res.Outputs {
		result[i] = output.Embedding
	}
	return result, nil
}
