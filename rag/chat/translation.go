package chat

import (
	"context"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/rag"
)

const translationDefaultTemplate = `Given a user query, translate it to {{.Target}}.
If the query is already in {{.Target}}, return it unchanged.
If you don't know the language of the query, return it unchanged.
Do not add explanations nor any other text.

Original query: {{.Query}}

Translated query:`

type TranslationTransformerConfig struct {
	Model corechat.Model

	// TargetLanguage is the language the embedding model expects —
	// "English", "Chinese", "Spanish", etc. Required.
	TargetLanguage string

	// PromptTemplate replaces the built-in prompt and must declare
	// {{.Target}} and {{.Query}}.
	PromptTemplate *chatclient.Template
}

var _ rag.Transformer = (*TranslationTransformer)(nil)

type TranslationTransformer struct {
	transformer targetedTextTransformer
}

func NewTranslationTransformer(config TranslationTransformerConfig) (*TranslationTransformer, error) {
	transformer, err := newTargetedTextTransformer(
		config.Model,
		config.PromptTemplate,
		translationDefaultTemplate,
		config.TargetLanguage,
		"translation target language",
	)
	if err != nil {
		return nil, err
	}

	return &TranslationTransformer{transformer: transformer}, nil
}

func (t *TranslationTransformer) Transform(ctx context.Context, query rag.Query) (rag.Query, error) {
	return t.transformer.transform(ctx, query)
}
