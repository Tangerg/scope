package openai

import (
	"errors"
	"fmt"
	"strconv"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

func optionalCount(field respjson.Field) (*int64, error) {
	raw := field.Raw()
	if raw == "" || raw == "null" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, errors.New("expected a nonnegative integer token count")
	}
	return &value, nil
}

func mapUsage(u openaisdk.CompletionUsage) (*corechat.Usage, error) {
	return projectUsage(u.JSON.PromptTokens, u.JSON.CompletionTokens, u.CompletionTokensDetails.JSON.ReasoningTokens, u.PromptTokensDetails.JSON.CachedTokens, respjson.Field{})
}

func responsesUsage(u responses.ResponseUsage) (*corechat.Usage, error) {
	return projectUsage(u.JSON.InputTokens, u.JSON.OutputTokens, u.OutputTokensDetails.JSON.ReasoningTokens, u.InputTokensDetails.JSON.CachedTokens, u.InputTokensDetails.JSON.CacheWriteTokens)
}

func projectUsage(input, output, reasoning, read, write respjson.Field) (*corechat.Usage, error) {
	var in, out *int64
	usage := &corechat.Usage{}
	for _, field := range []struct {
		name   string
		native respjson.Field
		dest   **int64
	}{
		{"input", input, &in}, {"output", output, &out}, {"reasoning", reasoning, &usage.ReasoningTokens},
		{"cache read", read, &usage.CacheReadInputTokens}, {"cache write", write, &usage.CacheWriteInputTokens},
	} {
		value, err := optionalCount(field.native)
		if err != nil {
			return nil, fmt.Errorf("%w: openai: %w: %s tokens: %w", corechat.ErrInvalidResponse, corechat.ErrInvalidUsage, field.name, err)
		}
		*field.dest = value
	}
	if in == nil || out == nil {
		return nil, nil
	}
	usage.InputTokens, usage.OutputTokens = *in, *out
	return usage, nil
}
