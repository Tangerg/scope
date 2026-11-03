// Package tool exposes rag retrieval through the ordinary core Tool contract.
// A Retrieval accepts an already composed retriever and owns strict argument
// decoding and candidate result encoding without an Agent-specific API.
package tool

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	coretool "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/rag"
)

// RetrievalConfig describes a model-visible retrieval capability. Name
// and Description follow [coretool.Definition] requirements. Retriever is
// required and may already be composed with transformers, expansion, fusion,
// and refiners.
type RetrievalConfig struct {
	Name        string
	Description string
	Retriever   rag.Retriever
}

type RetrievalRequest struct {
	Query string `json:"query" jsonschema:"minLength=1" jsonschema_description:"Natural-language query to retrieve evidence for."`
}

type RetrievalOutput struct {
	Candidates rag.Candidates `json:"candidates"`
}

// NewRetrieval adapts a [rag.Retriever] to the ordinary [coretool.Tool]
// contract. It can be advertised immediately or placed in an agent's
// DeferredTools set.
func NewRetrieval(config RetrievalConfig) (coretool.Func[RetrievalRequest, RetrievalOutput], error) {
	if lo.IsNil(config.Retriever) {
		return coretool.Func[RetrievalRequest, RetrievalOutput]{}, rag.ErrNilRetriever
	}
	return coretool.NewFunc(
		coretool.FuncConfig{Name: config.Name, Description: config.Description, Effect: coretool.EffectSafe},
		func(ctx context.Context, input RetrievalRequest) (RetrievalOutput, error) {
			query, err := rag.NewQuery(input.Query)
			if err != nil {
				return RetrievalOutput{}, inputFailure(fmt.Errorf("rag: retrieval tool query: %w", err))
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
}

// inputFailure reports a query rejected before the retriever runs as a definite,
// model-visible Tool failure rather than an uncertain error.
func inputFailure(cause error) error {
	failure, err := coretool.NewFailure(coretool.FailureConfig{
		Kind:   coretool.FailureKindFailed,
		Output: coretool.NewTextOutput(cause.Error()),
		Cause:  cause,
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}
