package vector

import (
	"context"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

type EncoderConfig struct {
	Space     vectorstore.SpaceID
	Documents embeddingclient.Config
	Queries   embeddingclient.Config
}

// Encoder binds both halves of a compatible vector space. The Host identifies
// the deployed revisions and preprocessing; neither a model name nor a vector
// width is an adequate substitute for that binding.
type Encoder struct {
	space     vectorstore.SpaceID
	documents embeddingclient.Client
	queries   embeddingclient.Client
}

func NewEncoder(config EncoderConfig) (*Encoder, error) {
	if err := config.Space.Validate(); err != nil {
		return nil, err
	}
	documents, err := embeddingclient.New(config.Documents)
	if err != nil {
		return nil, err
	}
	queries, err := embeddingclient.New(config.Queries)
	if err != nil {
		return nil, err
	}
	return &Encoder{space: config.Space, documents: documents, queries: queries}, nil
}

func (e *Encoder) Space() vectorstore.SpaceID { return e.space }

func (e *Encoder) EncodeDocuments(ctx context.Context, texts []string) ([][]float64, error) {
	return e.documents.EmbedTexts(ctx, texts)
}

// EncodeQuery returns coordinates that can be reused across compatible native
// indexes. Lexical text is retained only when the selected search requires it.
func (e *Encoder) EncodeQuery(ctx context.Context, text string, options vectorstore.SearchOptions) (*vectorstore.Query, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	options = options.Clone()
	values, err := e.queries.EmbedText(ctx, text)
	if err != nil {
		return nil, err
	}
	q := &vectorstore.Query{Space: e.space, Values: values, Options: options}
	if options.EffectiveMode() == vectorstore.SearchModeHybrid {
		q.LexicalText = text
	}
	if checkErr := q.ValidateFor(e.space); checkErr != nil {
		return nil, checkErr
	}
	return q, nil
}
