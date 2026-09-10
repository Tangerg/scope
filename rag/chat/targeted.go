package chat

import (
	"context"
	"fmt"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/rag"
)

type targetedTextTransformer struct {
	prompt textModelPrompt
	target string
}

type targetedPromptVariables struct {
	Target string
	Query  string
}

func newTargetedTextTransformer(
	model corechat.Model,
	template *chatclient.Template,
	fallback string,
	target string,
	targetLabel string,
) (targetedTextTransformer, error) {
	if strings.TrimSpace(target) == "" {
		return targetedTextTransformer{}, fmt.Errorf("rag: %s is required", targetLabel)
	}
	if target != strings.TrimSpace(target) {
		return targetedTextTransformer{}, fmt.Errorf("rag: %s must not have surrounding whitespace", targetLabel)
	}
	prompt, err := newTextModelPrompt(
		model,
		template,
		fallback,
		promptVariableTarget,
		promptVariableQuery,
	)
	if err != nil {
		return targetedTextTransformer{}, err
	}
	return targetedTextTransformer{prompt: prompt, target: target}, nil
}

func (t targetedTextTransformer) transform(ctx context.Context, query rag.Query) (rag.Query, error) {
	if err := query.Validate(); err != nil {
		return rag.Query{}, err
	}
	text, err := t.prompt.call(ctx, targetedPromptVariables{
		Target: t.target,
		Query:  query.Text(),
	})
	if err != nil {
		return rag.Query{}, err
	}
	return query.WithText(text)
}
