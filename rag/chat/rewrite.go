package chat

import (
	"context"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/rag"
)

// rewriteDefaultTemplate asks the LLM to rewrite the query to be
// concise, specific, and tuned to a particular search target.
// {{.Target}} and {{.Query}} are filled at transform time.
const rewriteDefaultTemplate = `Given a user query, rewrite it to provide better results when querying a {{.Target}}.
Remove any irrelevant information, and ensure the query is concise and specific.

Original query:
{{.Query}}

Rewritten query:`

// RewriteTransformerConfig binds one model and prompt policy to query rewriting.
type RewriteTransformerConfig struct {
	// Model performs the rewrite. Required.
	Model corechat.Model

	// TargetSearchSystem names the downstream search engine — "vector
	// store", "web search engine", "database", etc. Required.
	TargetSearchSystem string

	// PromptTemplate is the LLM prompt. Defaults to
	// [rewriteDefaultTemplate]. Custom templates must declare
	// {{.Target}} and {{.Query}}.
	PromptTemplate *chatclient.Template
}

var _ rag.Transformer = (*RewriteTransformer)(nil)

// RewriteTransformer tightens a query for a configured search target.
type RewriteTransformer struct {
	transformer targetedTextTransformer
}

// NewRewriteTransformer freezes options while preserving query-scoped values.
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

// Transform asks the LLM to rewrite the query and returns a clone with Text
// replaced by the model output.
func (r *RewriteTransformer) Transform(ctx context.Context, query rag.Query) (rag.Query, error) {
	return r.transformer.transform(ctx, query)
}
