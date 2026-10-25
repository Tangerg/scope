package openai

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
)

// ResponsesDialect binds one provider's Responses-compatible endpoint. The
// callback runs after Core projection and before transport. Raw request fields
// are provider-namespaced; adapters with a typed extension can disable them.
type ResponsesDialect struct {
	Provider                   string
	PrepareRequest             func(*corechat.Request, *CompatibleResponsesRequest) error
	DisableRawRequestExtension bool
	PreserveStreamEvents       bool
}

func (r ResponsesDialect) Validate() error { return validateProvider(r.Provider) }

// CompatibleResponsesRequest exposes only provider-owned Responses controls.
// BuiltInTools are appended to Core's function tools; a non-nil empty slice
// explicitly sends that remaining tool list. SDK wire types stay private.
type CompatibleResponsesRequest struct {
	Include      []string
	Store        *bool
	BuiltInTools []json.RawMessage
	extraFields  map[string]any
}

func (c CompatibleResponsesRequest) Validate() error {
	for index, raw := range c.BuiltInTools {
		var tool struct {
			Type string `json:"type"`
		}
		if err := jsonv2.Unmarshal(raw, &tool); err != nil {
			return fmt.Errorf("openai responses: built-in tools[%d]: %w", index, err)
		}
		if tool.Type == "" || tool.Type == "function" {
			return fmt.Errorf("openai responses: built-in tools[%d] must name a native tool; function tools belong to request.Tools", index)
		}
	}
	return nil
}

func (c *CompatibleResponsesRequest) SetExtraField(name string, value any) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return errors.New("openai responses: extra field name must be nonempty without surrounding whitespace")
	}
	switch name {
	case "model", "input", "tools", "max_output_tokens", "temperature", "top_p", "stream", "parallel_tool_calls", "tool_choice", "reasoning", "text", "include", "store":
		return fmt.Errorf("openai responses: extra field %q already has a typed owner", name)
	}
	if c.extraFields == nil {
		c.extraFields = make(map[string]any)
	}
	c.extraFields[name] = value
	return nil
}
