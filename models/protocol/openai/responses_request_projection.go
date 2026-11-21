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
	if err = dialect.validateOptions(options); err != nil {
		return nil, err
	}
	native, err := dialect.requestExtension(options.Extensions)
	if err != nil {
		return nil, err
	}
	resolved := request.Clone()
	resolved.Options = options
	return &responsesRequestProjection{request: resolved, dialect: dialect, native: native}, nil
}

func (r *responsesRequestProjection) project() (*responses.ResponseNewParams, error) {
	if err := r.projectOptions(); err != nil {
		return nil, err
	}
	r.projectToolChoice()
	items, err := mapResponsesInput(r.request.Messages, responsesReasoningCodec{provider: r.dialect.Provider})
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
	r.native.Reasoning.Effort = openaisdk.ReasoningEffort(options.ReasoningEffort)
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
	case corechat.ToolChoiceAuto:
		r.native.ToolChoice.OfToolChoiceMode = openaisdk.Opt(responses.ToolChoiceOptionsAuto)
	case corechat.ToolChoiceNone:
		r.native.ToolChoice.OfToolChoiceMode = openaisdk.Opt(responses.ToolChoiceOptionsNone)
	case corechat.ToolChoiceRequired:
		r.native.ToolChoice.OfToolChoiceMode = openaisdk.Opt(responses.ToolChoiceOptionsRequired)
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
