package chat

import (
	"context"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/rag"
)

// translationDefaultTemplate asks the LLM to translate the query into
// the target language, returning the original unchanged when it's
// already in that language or when language detection is uncertain.
// {{.Target}} and {{.Query}} are filled at transform time.
const translationDefaultTemplate = `Given a user query, translate it to {{.Target}}.
If the query is already in {{.Target}}, return it unchanged.
If you don't know the language of the query, return it unchanged.
Do not add explanations nor any other text.

Original query: {{.Query}}

Translated query:`

// TranslationTransformerConfig binds one model and target language without
// changing retrieval-scoped query values.
type TranslationTransformerConfig struct {
	// Model performs the translation. Required.
	Model corechat.Model

	// TargetLanguage is the language the embedding model expects —
	// "English", "Chinese", "Spanish", etc. Required.
	TargetLanguage string

	// PromptTemplate is the LLM prompt. Defaults to
	// [translationDefaultTemplate]. Custom templates must declare
	// {{.Target}} and {{.Query}}.
	PromptTemplate *chatclient.Template
}

var _ rag.Transformer = (*TranslationTransformer)(nil)

// TranslationTransformer translates queries into a configured language.
type TranslationTransformer struct {
	transformer targetedTextTransformer
}

// NewTranslationTransformer validates target-language and model policy once.
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

// Transform asks the LLM to translate the query and returns a clone with Text
// replaced by the model output.
func (t *TranslationTransformer) Transform(ctx context.Context, query rag.Query) (rag.Query, error) {
	return t.transformer.transform(ctx, query)
}
