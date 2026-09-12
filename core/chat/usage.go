package chat

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/internal/ptr"
)

// ErrInvalidUsage identifies impossible token totals or breakdowns.
var ErrInvalidUsage = errors.New("chat: invalid usage")

// Usage records provider-neutral token counts. Breakdown pointers distinguish
// an explicitly reported zero from an unsupported dimension.
type Usage struct {
	// InputTokens is the total processed input count. Provider cache-read and
	// cache-write counts, when reported, are breakdowns included in this total.
	InputTokens           int64  `json:"input_tokens,omitempty"`
	OutputTokens          int64  `json:"output_tokens,omitempty"`
	ReasoningTokens       *int64 `json:"reasoning_tokens,omitempty"`
	CacheReadInputTokens  *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`
}

// TotalTokens returns the provider-reported input and output totals without
// double-counting their optional breakdowns.
func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 {
		return fmt.Errorf("%w: input_tokens must not be negative", ErrInvalidUsage)
	}
	if u.OutputTokens < 0 {
		return fmt.Errorf("%w: output_tokens must not be negative", ErrInvalidUsage)
	}
	if u.InputTokens > 0 && u.OutputTokens > (1<<63-1)-u.InputTokens {
		return fmt.Errorf("%w: total token count overflows int64", ErrInvalidUsage)
	}
	if err := validateTokenSubset("reasoning_tokens", u.ReasoningTokens, u.OutputTokens); err != nil {
		return err
	}
	if err := validateTokenSubset("cache_read_input_tokens", u.CacheReadInputTokens, u.InputTokens); err != nil {
		return err
	}
	if err := validateTokenSubset("cache_write_input_tokens", u.CacheWriteInputTokens, u.InputTokens); err != nil {
		return err
	}
	return nil
}

func (u Usage) clone() Usage {
	clone := u
	clone.ReasoningTokens = ptr.Clone(u.ReasoningTokens)
	clone.CacheReadInputTokens = ptr.Clone(u.CacheReadInputTokens)
	clone.CacheWriteInputTokens = ptr.Clone(u.CacheWriteInputTokens)
	return clone
}

func validateTokenSubset(name string, value *int64, total int64) error {
	if value == nil {
		return nil
	}
	if *value < 0 || *value > total {
		return fmt.Errorf("%w: %s must be between zero and its total", ErrInvalidUsage, name)
	}
	return nil
}

func (u Usage) MarshalJSON() ([]byte, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	type wireUsage Usage
	return json.Marshal(wireUsage(u))
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	if u == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidUsage)
	}
	type wireUsage Usage
	var decoded wireUsage
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidUsage, err)
	}
	candidate := Usage(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*u = candidate
	return nil
}
