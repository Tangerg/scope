package openai

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	corechat "github.com/Tangerg/scope/core/chat"
)

type responsesRequestProjection struct {
	request *corechat.Request
	dialect ResponsesDialect
	native  responses.ResponseNewParams
}

func newResponsesRequestProjection(request *corechat.Request, defaults corechat.Options, dialect ResponsesDialect) (*responsesRequestProjection, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("openai responses: request: %w", err)
	}
	options, err := defaults.Resolve(request.Options)
	if err != nil {
		return nil, fmt.Errorf("openai responses: options: %w", err)
	}
	resolved := *request
	resolved.Options = options
	extensionKey := dialect.Provider + "/responses_request"
	if ownershipErr := rejectCoreOwnedResponsesExtension(options.Extensions, extensionKey); ownershipErr != nil {
		return nil, ownershipErr
	}
	native, found, err := options.Extensions.Decode[responses.ResponseNewParams](extensionKey)
	if err != nil {
		return nil, fmt.Errorf("openai responses: extension %q: %w", extensionKey, err)
	}
	if found && dialect.DisableRawRequestExtension {
		return nil, fmt.Errorf("%s: raw Responses request extension is not supported", dialect.Provider)
	}
	return &responsesRequestProjection{request: &resolved, dialect: dialect, native: native}, nil
}

func (r *responsesRequestProjection) project() (*responses.ResponseNewParams, error) {
	if err := r.projectOptions(); err != nil {
		return nil, err
	}
	r.projectToolChoice()
	items, err := mapResponsesInput(r.request.Messages)
	if err != nil {
		return nil, err
	}
	r.native.Input.OfInputItemList = items
	r.native.Tools, err = mapResponsesTools(r.request.Tools)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(r.native.Include, responses.ResponseIncludableReasoningEncryptedContent) {
		r.native.Include = append(r.native.Include, responses.ResponseIncludableReasoningEncryptedContent)
	}
	if err := r.projectDialect(); err != nil {
		return nil, err
	}
	return &r.native, nil
}

func (r *responsesRequestProjection) projectOptions() error {
	options := r.request.Options
	if options.Model == "" {
		return errors.New("openai responses: model is required in defaults or request options")
	}
	if options.FrequencyPenalty != nil || options.PresencePenalty != nil || options.TopK != nil || len(options.Stop) != 0 {
		return errors.New("openai responses: frequency_penalty, presence_penalty, top_k, and stop are not supported")
	}
	r.native.Model = shared.ResponsesModel(options.Model)
	if options.MaxOutputTokens != nil {
		r.native.MaxOutputTokens = openaisdk.Int(*options.MaxOutputTokens)
	}
	if options.Temperature != nil {
		r.native.Temperature = openaisdk.Float(*options.Temperature)
	}
	if options.TopP != nil {
		r.native.TopP = openaisdk.Float(*options.TopP)
	}
	effort, err := mapReasoningEffort(options.ReasoningEffort)
	if err != nil {
		return err
	}
	r.native.Reasoning.Effort = effort
	if options.OutputFormat != nil {
		format, err := mapResponsesOutputFormat(options.OutputFormat)
		if err != nil {
			return err
		}
		r.native.Text.Format = format
	}
	return nil
}

func (r *responsesRequestProjection) projectToolChoice() {
	choice := r.request.ToolChoice
	if choice == nil {
		return
	}
	switch choice.Mode {
	case corechat.ToolChoiceAuto, corechat.ToolChoiceNone, corechat.ToolChoiceRequired:
		r.native.ToolChoice.OfToolChoiceMode = openaisdk.Opt(responses.ToolChoiceOptions(choice.Mode))
	case corechat.ToolChoiceNamed:
		r.native.ToolChoice.OfFunctionTool = &responses.ToolChoiceFunctionParam{Name: choice.Name}
	}
	switch choice.Parallelism {
	case corechat.ToolParallelismAllow:
		r.native.ParallelToolCalls = openaisdk.Bool(true)
	case corechat.ToolParallelismSingle:
		r.native.ParallelToolCalls = openaisdk.Bool(false)
	}
}

func (r *responsesRequestProjection) projectDialect() error {
	if r.dialect.PrepareRequest == nil {
		return nil
	}
	compatible := &CompatibleResponsesRequest{extraFields: maps.Clone(r.native.ExtraFields())}
	for _, include := range r.native.Include {
		compatible.Include = append(compatible.Include, string(include))
	}
	if r.native.Store.Valid() {
		compatible.Store = new(r.native.Store.Value)
	}
	if err := r.dialect.PrepareRequest(r.request, compatible); err != nil {
		return err
	}
	if err := compatible.Validate(); err != nil {
		return err
	}
	r.native.Include = nil
	for _, include := range compatible.Include {
		r.native.Include = append(r.native.Include, responses.ResponseIncludable(include))
	}
	r.native.Store = param.Opt[bool]{}
	if compatible.Store != nil {
		r.native.Store = openaisdk.Bool(*compatible.Store)
	}
	for _, raw := range compatible.BuiltInTools {
		r.native.Tools = append(r.native.Tools, param.Override[responses.ToolUnionParam](raw))
	}
	if compatible.BuiltInTools != nil {
		if compatible.extraFields == nil {
			compatible.extraFields = make(map[string]any)
		}
		compatible.extraFields[string(responsesFieldTools)] = r.native.Tools
	}
	r.native.SetExtraFields(compatible.extraFields)
	return nil
}
