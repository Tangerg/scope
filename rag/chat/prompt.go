package chat

import (
	"context"
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/rag"
)

const (
	promptVariableContext    = "Context"
	promptVariableCandidates = "Candidates"
	promptVariableHistory    = "History"
	promptVariableNumber     = "Number"
	promptVariableQuery      = "Query"
	promptVariableTarget     = "Target"
)

// ErrEmptyModelOutput classifies a model response that [rag.Query] will not
// accept as query text, such as text that is empty or blank after trimming.
// Query owns that admission rule; this sentinel only records that the rejected
// text came from the model, so a caller can tell it apart from its own invalid
// query. Missing response text and unsuccessful completion preserve the
// chatclient errors.
var ErrEmptyModelOutput = errors.New("rag: model returned empty query text")

// queryFromModel folds model-produced text into query. The caller must pass an
// already-validated query; Query then owns trimming and the non-empty rule, and
// a response Query rejects is wrapped as [ErrEmptyModelOutput].
func queryFromModel(query rag.Query, text string) (rag.Query, error) {
	updated, err := query.WithText(text)
	if err != nil {
		return rag.Query{}, fmt.Errorf("%w: %w", ErrEmptyModelOutput, err)
	}
	return updated, nil
}

// modelPrompt owns the common template and typed output boundary used by
// every model-backed RAG component.
type modelPrompt[T any] struct {
	client   chatclient.Client
	format   chatclient.OutputFormat[T]
	template *chatclient.Template
}

func newModelPrompt[T any](
	model corechat.Model,
	format chatclient.OutputFormat[T],
	template *chatclient.Template,
	fallback string,
	required ...string,
) (modelPrompt[T], error) {
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		return modelPrompt[T]{}, err
	}
	template, err = resolvePromptTemplate(template, fallback, required...)
	if err != nil {
		return modelPrompt[T]{}, err
	}
	return modelPrompt[T]{client: client, format: format, template: template}, nil
}

func resolvePromptTemplate(current *chatclient.Template, fallback string, required ...string) (*chatclient.Template, error) {
	if current == nil {
		var err error
		current, err = chatclient.ParseTemplate(fallback)
		if err != nil {
			return nil, err
		}
	}
	if err := current.Require(required...); err != nil {
		return nil, err
	}
	return current, nil
}

func (m modelPrompt[T]) call(ctx context.Context, data any) (T, error) {
	var zero T
	message, err := m.template.UserMessage(data)
	if err != nil {
		return zero, err
	}
	return m.client.Output(ctx, &corechat.Request{Messages: []corechat.Message{message}}, m.format)
}
