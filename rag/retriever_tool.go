package rag

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// RetrievalToolConfig describes a model-visible retrieval capability. Name
// and Description follow [chat.ToolDefinition] requirements. Retriever is
// required and may already be composed with transformers, expansion, fusion,
// and refiners.
type RetrievalToolConfig struct {
	Name        string
	Description string
	Retriever   Retriever
}

// RetrievalToolRequest is the strict model-generated tool input.
type RetrievalToolRequest struct {
	Query string `json:"query" jsonschema:"minLength=1" jsonschema_description:"Natural-language query to retrieve evidence for."`
}

// RetrievalToolOutput is the model-visible retrieval result.
type RetrievalToolOutput struct {
	Candidates Candidates `json:"candidates"`
}

// RetrievalTool adapts a [Retriever] to the ordinary [tool.Tool] contract. It
// can be advertised immediately or placed in an agent's DeferredTools set.
type RetrievalTool struct {
	function tool.Func[RetrievalToolRequest, RetrievalToolOutput]
}

var _ tool.Tool = RetrievalTool{}

// NewRetrievalTool exposes a composed Retriever through the ordinary core tool
// contract without adding Agent-specific retrieval semantics.
func NewRetrievalTool(config RetrievalToolConfig) (RetrievalTool, error) {
	if lo.IsNil(config.Retriever) {
		return RetrievalTool{}, ErrNilRetriever
	}
	function, err := tool.NewFunc(
		tool.FuncConfig{Name: config.Name, Description: config.Description},
		func(ctx context.Context, input RetrievalToolRequest) (RetrievalToolOutput, error) {
			query, err := NewQuery(input.Query)
			if err != nil {
				return RetrievalToolOutput{}, fmt.Errorf("rag: retrieval tool query: %w", err)
			}
			candidates, err := retrieve(ctx, query, config.Retriever.Retrieve)
			if err != nil {
				return RetrievalToolOutput{}, err
			}
			return RetrievalToolOutput{Candidates: candidates}, nil
		},
	)
	if err != nil {
		return RetrievalTool{}, err
	}
	return RetrievalTool{function: function}, nil
}

func (r RetrievalTool) Definition() chat.ToolDefinition { return r.function.Definition() }

func (r RetrievalTool) Call(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	return r.function.Call(ctx, invocation)
}
