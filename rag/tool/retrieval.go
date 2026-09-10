// Package tool exposes rag retrieval through the ordinary core Tool contract.
// A Retrieval accepts an already composed retriever and owns strict argument
// decoding and candidate result encoding without an Agent-specific API.
package tool

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	coretool "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/rag"
)

// RetrievalConfig describes a model-visible retrieval capability. Name
// and Description follow [chat.ToolDefinition] requirements. Retriever is
// required and may already be composed with transformers, expansion, fusion,
// and refiners.
type RetrievalConfig struct {
	Name        string
	Description string
	Retriever   rag.Retriever
}

// RetrievalRequest is the strict model-generated tool input.
type RetrievalRequest struct {
	Query string `json:"query" jsonschema:"minLength=1" jsonschema_description:"Natural-language query to retrieve evidence for."`
}

// RetrievalOutput is the model-visible retrieval result.
type RetrievalOutput struct {
	Candidates rag.Candidates `json:"candidates"`
}

// Retrieval adapts a [rag.Retriever] to the ordinary [coretool.Tool] contract. It
// can be advertised immediately or placed in an agent's DeferredTools set.
type Retrieval struct {
	function coretool.Func[RetrievalRequest, RetrievalOutput]
}

var _ coretool.Tool = Retrieval{}

// NewRetrieval exposes a composed Retriever through the ordinary core tool
// contract without adding Agent-specific retrieval semantics.
func NewRetrieval(config RetrievalConfig) (Retrieval, error) {
	if lo.IsNil(config.Retriever) {
		return Retrieval{}, rag.ErrNilRetriever
	}
	function, err := coretool.NewFunc(
		coretool.FuncConfig{Name: config.Name, Description: config.Description},
		func(ctx context.Context, input RetrievalRequest) (RetrievalOutput, error) {
			query, err := rag.NewQuery(input.Query)
			if err != nil {
				return RetrievalOutput{}, fmt.Errorf("rag: retrieval tool query: %w", err)
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RetrievalOutput{}, ctxErr
			}
			candidates, err := config.Retriever.Retrieve(ctx, query)
			if err != nil {
				return RetrievalOutput{}, err
			}
			if candidateErr := candidates.Validate(); candidateErr != nil {
				return RetrievalOutput{}, candidateErr
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RetrievalOutput{}, ctxErr
			}
			return RetrievalOutput{Candidates: candidates}, nil
		},
	)
	if err != nil {
		return Retrieval{}, err
	}
	return Retrieval{function: function}, nil
}

func (r Retrieval) Definition() chat.ToolDefinition { return r.function.Definition() }

func (r Retrieval) Call(ctx context.Context, invocation coretool.Invocation) (chat.ToolOutput, error) {
	return r.function.Call(ctx, invocation)
}
