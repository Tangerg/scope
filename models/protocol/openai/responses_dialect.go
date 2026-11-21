package openai

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared/constant"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

// ResponsesDialect binds one provider's Responses-compatible endpoint. The
// callback runs after Core projection and before transport. Raw request fields
// are provider-namespaced; adapters with a typed extension can disable them.
// PrepareRequest receives an independently owned request with resolved options.
// Only target changes affect the wire request; source edits never change the
// caller's history.
type ResponsesDialect struct {
	Provider                   string
	PrepareRequest             func(*corechat.Request, *CompatibleResponsesRequest) error
	DisableRawRequestExtension bool
	PreserveStreamEvents       bool
}

func (r ResponsesDialect) Validate() error { return metadata.ValidateExtensionNamespace(r.Provider) }

func (r ResponsesDialect) validateOptions(options corechat.Options) error {
	if options.FrequencyPenalty != nil || options.PresencePenalty != nil || options.TopK != nil || len(options.Stop) != 0 {
		return errors.New("openai responses: frequency_penalty, presence_penalty, top_k, and stop are not supported")
	}
	if err := validateReasoningEffort(options.ReasoningEffort, openAIReasoningEfforts); err != nil {
		return err
	}
	if options.OutputFormat != nil {
		if _, err := mapResponsesOutputFormat(options.OutputFormat); err != nil {
			return err
		}
	}
	_, err := r.requestExtension(options.Extensions)
	return err
}

// requestExtension returns the native request a caller supplied under the
// provider's extension key; it may not carry a field the Core projection owns.
func (r ResponsesDialect) requestExtension(extensions metadata.Extensions) (responses.ResponseNewParams, error) {
	extensionKey := r.Provider + ResponsesRequestExtensionSuffix
	if err := rejectCoreOwnedResponsesExtension(extensions, extensionKey); err != nil {
		return responses.ResponseNewParams{}, err
	}
	native, found, err := extensions.Decode[responses.ResponseNewParams](extensionKey)
	if err != nil {
		return responses.ResponseNewParams{}, fmt.Errorf("openai responses: extension %q: %w", extensionKey, err)
	}
	if found && r.DisableRawRequestExtension {
		return responses.ResponseNewParams{}, fmt.Errorf("%s: raw Responses request extension is not supported", r.Provider)
	}
	return native, nil
}

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
		if tool.Type == "" || tool.Type == string(constant.ValueOf[constant.Function]()) {
			return fmt.Errorf("openai responses: built-in tools[%d] must name a native tool; function tools belong to request.Tools", index)
		}
	}
	return nil
}

func (c *CompatibleResponsesRequest) SetExtraField(name string, value any) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return errors.New("openai responses: extra field name must be nonempty without surrounding whitespace")
	}
	if responsesRequestField(name).typedOwner() {
		return fmt.Errorf("openai responses: extra field %q already has a typed owner", name)
	}
	if c.extraFields == nil {
		c.extraFields = make(map[string]any)
	}
	c.extraFields[name] = value
	return nil
}
