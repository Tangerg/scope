package rerank

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// Options holds per-request reranking configuration. TopK zero means every
// document; provider-specific controls remain in Extensions.
type Options struct {
	Model      string              `json:"model"`
	TopK       int                 `json:"top_k,omitempty"`
	Extensions metadata.Extensions `json:"extensions,omitzero"`
}

func (o Options) Clone() Options {
	return Options{Model: o.Model, TopK: o.TopK, Extensions: o.Extensions.Clone()}
}

func (o Options) Resolve(override Options) (Options, error) {
	effective := o.Clone()
	if override.Model != "" {
		effective.Model = override.Model
	}
	if override.TopK != 0 {
		effective.TopK = override.TopK
	}
	if !override.Extensions.IsZero() {
		if err := effective.Extensions.Merge(override.Extensions); err != nil {
			return Options{}, fmt.Errorf("rerank: resolve options: %w: merge extensions: %w", ErrInvalidOptions, err)
		}
	}
	if err := effective.Validate(); err != nil {
		return Options{}, fmt.Errorf("rerank: resolve options: %w", err)
	}
	return effective, nil
}

func (o Options) Validate() error {
	if o.Model != "" && strings.TrimSpace(o.Model) != o.Model {
		return fmt.Errorf("%w: model id must not have surrounding whitespace", ErrInvalidOptions)
	}
	if o.TopK < 0 {
		return fmt.Errorf("%w: top K must not be negative", ErrInvalidOptions)
	}
	if err := o.Extensions.Validate(); err != nil {
		return fmt.Errorf("%w: extensions: %w", ErrInvalidOptions, err)
	}
	return nil
}

func (o Options) ResultLimit(documentCount int) int {
	if o.TopK == 0 {
		return documentCount
	}
	return o.TopK
}

func (o Options) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wireOptions Options
	return json.Marshal(wireOptions(o))
}

func (o *Options) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: options receiver is nil", ErrInvalidOptions)
	}
	type wireOptions Options
	var decoded wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode options: %w", ErrInvalidOptions, err)
	}
	candidate := Options(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}
