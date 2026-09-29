package chat

import (
	"context"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/rag"
)

const rewriteDefaultTemplate = `Given a user query, rewrite it to provide better results when querying a {{.Target}}.
Remove any irrelevant information, and ensure the query is concise and specific.

Original query:
{{.Query}}

Rewritten query:`

type RewriteTransformerConfig struct {
	Model corechat.Model

	// TargetSearchSystem names the downstream search engine — "vector
	// store", "web search engine", "database", etc. Required.
	TargetSearchSystem string

	// PromptTemplate replaces the built-in prompt and must declare
	// {{.Target}} and {{.Query}}.
	PromptTemplate *chatclient.Template
}

var _ rag.Transformer = (*RewriteTransformer)(nil)

type RewriteTransformer struct {
	transformer targetedTextTransformer
}

func NewRewriteTransformer(config RewriteTransformerConfig) (*RewriteTransformer, error) {
	transformer, err := newTargetedTextTransformer(
		config.Model,
		config.PromptTemplate,
		rewriteDefaultTemplate,
		config.TargetSearchSystem,
		"rewrite target",
	)
	if err != nil {
		return nil, err
	}

	return &RewriteTransformer{transformer: transformer}, nil
}

func (r *RewriteTransformer) Transform(ctx context.Context, query rag.Query) (rag.Query, error) {
	return r.transformer.transform(ctx, query)
}
