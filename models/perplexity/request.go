package perplexity

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// RequestOptions binds Agent API fields without a neutral Core equivalent.
// Effort configures Perplexity's agent loop (fast, low, medium, high, xhigh,
// optionally @latest or @version); Options.ReasoningEffort independently
// configures the selected model. Tools contains native built-in tool objects;
// function tools belong to chat.Request.Tools. Store controls retrieval, not
// whether Perplexity retains conversation data. A non-nil empty Tools slice
// explicitly disables native built-in tools while retaining Core function tools.
type RequestOptions struct {
	Effort   string            `json:"effort,omitzero"`
	MaxSteps *int64            `json:"max_steps,omitzero"`
	Store    *bool             `json:"store,omitzero"`
	Tools    []json.RawMessage `json:"tools,omitzero"`
}

func (r *RequestOptions) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("perplexity: nil RequestOptions")
	}
	type wire RequestOptions
	var value wire
	if err := jsonv2.Unmarshal(data, &value, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	*r = RequestOptions(value)
	return nil
}
func (r RequestOptions) Validate() error {
	if r.Effort != "" {
		name, version, versioned := strings.Cut(r.Effort, "@")
		switch name {
		case "fast", "low", "medium", "high", "xhigh":
		default:
			return fmt.Errorf("perplexity: unsupported agent effort %q", r.Effort)
		}
		if versioned && (version == "" || strings.ContainsAny(version, "@ \t\r\n")) {
			return fmt.Errorf("perplexity: invalid agent effort version %q", r.Effort)
		}
	}
	if r.MaxSteps != nil && *r.MaxSteps <= 0 {
		return errors.New("perplexity: max_steps must be positive")
	}
	return (openai.CompatibleResponsesRequest{BuiltInTools: r.Tools}).Validate()
}
func prepareRequest(source *corechat.Request, target *openai.CompatibleResponsesRequest) error {
	options, _, err := source.Options.Extensions.Decode[RequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("perplexity: Agent API extension: %w", err)
	}
	if err := options.Validate(); err != nil {
		return err
	}
	// Encrypted reasoning replay is an OpenAI-specific include value, not an
	// Agent API request requirement. Perplexity returns its native output items.
	target.Include = nil
	if options.Effort != "" {
		if err := target.SetExtraField("effort", options.Effort); err != nil {
			return err
		}
	}
	if options.MaxSteps != nil {
		if err := target.SetExtraField("max_steps", *options.MaxSteps); err != nil {
			return err
		}
	}
	target.Store = options.Store
	target.BuiltInTools = options.Tools
	return nil
}
