package anthropic

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Anthropic reports disjoint input counts and may update just one counter in
// a later event. Keep the observed native evidence; derive Core's inclusive
// total only when the input and output counts are both known.
type messagesUsage struct {
	Input   *int64 `json:"input_tokens"`
	Output  *int64 `json:"output_tokens"`
	Read    *int64 `json:"cache_read_input_tokens"`
	Write   *int64 `json:"cache_creation_input_tokens"`
	Details struct {
		Thinking *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func (m *messagesUsage) observe(raw string, dest *corechat.ResponseMetadata) error {
	if raw == "" || raw == "null" {
		return nil
	}
	var update messagesUsage
	if err := jsonv2.Unmarshal([]byte(raw), &update); err != nil {
		return fmt.Errorf("%w: anthropic: malformed usage report: %w", corechat.ErrInvalidResponse, err)
	}
	for _, value := range []*int64{update.Input, update.Output, update.Read, update.Write, update.Details.Thinking} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: anthropic: negative usage count", corechat.ErrInvalidResponse)
		}
	}
	if update.Input != nil {
		m.Input = update.Input
	}
	if update.Output != nil {
		m.Output = update.Output
	}
	if update.Read != nil {
		m.Read = update.Read
	}
	if update.Write != nil {
		m.Write = update.Write
	}
	if update.Details.Thinking != nil {
		m.Details.Thinking = update.Details.Thinking
	}
	return dest.Extra.Set(protocolUsageKey, json.RawMessage(raw))
}

func (m messagesUsage) snapshot() (*corechat.Usage, error) {
	if m.Input == nil || m.Output == nil {
		return nil, nil
	}
	total := *m.Input
	for _, value := range []*int64{m.Read, m.Write} {
		if value != nil {
			if *value > math.MaxInt64-total {
				return nil, fmt.Errorf("%w: anthropic: input token counts overflow", corechat.ErrInvalidResponse)
			}
			total += *value
		}
	}
	usage := corechat.Usage{InputTokens: total, OutputTokens: *m.Output, CacheReadInputTokens: m.Read, CacheWriteInputTokens: m.Write, ReasoningTokens: m.Details.Thinking}
	return new(usage.Clone()), nil
}
