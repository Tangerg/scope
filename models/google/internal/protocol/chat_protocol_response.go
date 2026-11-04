package protocol

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
)

const (
	// ResponseExtensionKey preserves the complete official GenerateContent
	// response (or the current official stream chunk).
	ResponseExtensionKey = GoogleProviderID + ResponseExtensionSuffix

	generatedToolIDNamespaceSuffix = "/generated/"
)

type protocolResponseMapper struct {
	partOffset   int
	provider     string
	generation   generationStream
	hasToolCalls bool
}

func newProtocolResponseMapper(provider string) *protocolResponseMapper {
	return &protocolResponseMapper{provider: provider}
}

func (p *protocolResponseMapper) mapDelta(requestModel string, response *genai.GenerateContentResponse) (*corechat.ResponseDelta, error) {
	next, err := p.generation.advance(response)
	if err != nil {
		if refusal, ok := errors.AsType[*promptBlockError](err); ok {
			return nil, refusal
		}
		return nil, fmt.Errorf("google: stream: %w: %w", corechat.ErrInvalidResponse, err)
	}
	metadata, err := p.mapMetadata(requestModel, response, next)
	if err != nil {
		return nil, err
	}
	mapped := &corechat.ResponseDelta{Metadata: metadata}
	if len(response.Candidates) == 1 {
		candidate := response.Candidates[0]
		if err := p.mapCandidateDelta(candidate, mapped); err != nil {
			return nil, fmt.Errorf("google: stream output: %w", err)
		}
	}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("google: mapped response delta: %w", err)
	}
	p.generation = next
	return mapped, nil
}

func (p *protocolResponseMapper) finished() bool {
	return p.generation.finished()
}

func (p *protocolResponseMapper) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || !p.finished() {
		return nil, fmt.Errorf("google: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	delta.FinishReason = normalizeProtocolFinishReason(p.generation.finishReason, p.hasToolCalls)
	if err := delta.Validate(); err != nil {
		return nil, fmt.Errorf("google: terminal stream response: %w", err)
	}
	return delta, nil
}

func (p *protocolResponseMapper) mapMetadata(requestModel string, response *genai.GenerateContentResponse, generation generationStream) (*corechat.ResponseMetadata, error) {
	modelName := generation.modelVersion
	if modelName == "" {
		modelName = requestModel
	}
	metadata := &corechat.ResponseMetadata{ID: generation.responseID, Model: modelName}
	if err := metadata.Extra.Set(protocolKey(p.provider, ResponseExtensionSuffix), response); err != nil {
		return nil, fmt.Errorf("google: preserve native response: %w", err)
	}
	if response.UsageMetadata != nil {
		metadata.Usage = mapProtocolUsage(response.UsageMetadata)
		if err := metadata.Extra.Set(protocolKey(p.provider, usageMetadataSuffix), protocolUsageExtensionFrom(response.UsageMetadata)); err != nil {
			return nil, err
		}
		if response.UsageMetadata.ToolUsePromptTokenCount != 0 {
			if err := metadata.Extra.Set(protocolKey(p.provider, toolUsePromptTokensMetadataSuffix), int64(response.UsageMetadata.ToolUsePromptTokenCount)); err != nil {
				return nil, err
			}
		}
	}
	if response.ModelVersion != "" {
		if err := metadata.Extra.Set(protocolKey(p.provider, modelVersionMetadataSuffix), response.ModelVersion); err != nil {
			return nil, err
		}
	}
	if response.PromptFeedback != nil {
		if err := metadata.Extra.Set(protocolKey(p.provider, promptFeedbackMetadataSuffix), response.PromptFeedback); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func (p *protocolResponseMapper) mapCandidateDelta(candidate *genai.Candidate, response *corechat.ResponseDelta) error {
	response.OutputMetadata = &corechat.OutputMetadata{}
	if isProtocolStopReason(candidate.FinishReason) {
		if err := response.OutputMetadata.Extra.Set(protocolKey(p.provider, nativeFinishReasonMetadataSuffix), candidate.FinishReason); err != nil {
			return err
		}
	}
	if len(candidate.SafetyRatings) > 0 {
		if err := response.OutputMetadata.Extra.Set(protocolKey(p.provider, safetyRatingsMetadataSuffix), candidate.SafetyRatings); err != nil {
			return err
		}
	}
	if candidate.FinishMessage != "" {
		if err := response.OutputMetadata.Extra.Set(protocolKey(p.provider, finishMessageMetadataSuffix), candidate.FinishMessage); err != nil {
			return err
		}
	}
	var parts []*genai.Part
	if candidate.Content != nil {
		parts = candidate.Content.Parts
	}
	offset := p.partOffset
	for partIndex, part := range parts {
		delta, kind, include, err := mapProtocolCandidatePartDelta(p.provider, offset+partIndex, part)
		if err != nil {
			return fmt.Errorf("content.parts[%d]: %w", partIndex, err)
		}
		if include {
			response.Parts = append(response.Parts, delta)
			p.hasToolCalls = p.hasToolCalls || kind == corechat.PartToolCall
		}
	}
	p.partOffset = offset + len(parts)
	for _, citation := range protocolCitations(candidate.CitationMetadata) {
		response.Parts = append(response.Parts, corechat.NewCitationDelta(citation))
	}
	return nil
}

func mapProtocolCandidatePartDelta(provider string, partIndex int, part *genai.Part) (corechat.PartDelta, corechat.PartKind, bool, error) {
	if err := validateGenerationPart(part); err != nil {
		return corechat.PartDelta{}, "", false, fmt.Errorf("%w: %w", corechat.ErrInvalidResponse, err)
	}
	var mapped corechat.PartDelta
	var kind corechat.PartKind
	switch {
	case part.FunctionCall != nil:
		if part.FunctionCall.Name == "" {
			return corechat.PartDelta{}, "", false, errors.New("function call has no name")
		}
		if len(part.FunctionCall.PartialArgs) != 0 {
			return corechat.PartDelta{}, "", false, errors.New("structured partial function arguments have no portable JSON-fragment representation")
		}
		arguments, err := protocolToolArguments(part.FunctionCall.Args)
		if err != nil {
			return corechat.PartDelta{}, "", false, fmt.Errorf("function call arguments: %w", err)
		}
		id := part.FunctionCall.ID
		if id == "" {
			id = fmt.Sprintf("%s%s%d", provider, generatedToolIDNamespaceSuffix, partIndex)
		}
		mapped = corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: id, Name: part.FunctionCall.Name, Arguments: arguments})
		kind = corechat.PartToolCall
	case part.InlineData != nil:
		value, err := media.NewBytes(part.InlineData.MIMEType, part.InlineData.Data)
		if err != nil {
			return corechat.PartDelta{}, "", false, err
		}
		value.Name = part.InlineData.DisplayName
		mapped = corechat.NewMediaDelta(value)
		kind = corechat.PartMedia
	case part.FileData != nil:
		value, err := media.NewURI(part.FileData.MIMEType, part.FileData.FileURI)
		if err != nil {
			return corechat.PartDelta{}, "", false, err
		}
		value.Name = part.FileData.DisplayName
		mapped = corechat.NewMediaDelta(value)
		kind = corechat.PartMedia
	case part.Thought:
		mapped = corechat.NewReasoningDelta(part.Text, part.ThoughtSignature)
		kind = corechat.PartReasoning
	case part.Text != "":
		mapped = corechat.NewTextDelta(part.Text)
		kind = corechat.PartText
	case len(part.ThoughtSignature) != 0:
		mapped = corechat.NewReasoningDelta("", part.ThoughtSignature)
		kind = corechat.PartReasoning
	default:
		return corechat.PartDelta{}, "", false, nil
	}
	state := newPartReplayState(part, kind, partIndex)
	if state.hasFields() {
		if err := mapped.Metadata.Set(protocolKey(provider, partReplayStateMetadataSuffix), state); err != nil {
			return corechat.PartDelta{}, "", false, fmt.Errorf("preserve native part: %w", err)
		}
	}
	return mapped, kind, true, nil
}

func protocolToolArguments(arguments map[string]any) (string, error) {
	if arguments == nil {
		return "", nil
	}
	return protocolJSON(arguments)
}

func protocolCitations(metadata *genai.CitationMetadata) []corecontent.Citation {
	if metadata == nil {
		return nil
	}
	result := make([]corecontent.Citation, 0, len(metadata.Citations))
	for _, citation := range metadata.Citations {
		if citation == nil || citation.URI == "" {
			continue
		}
		mapped := corecontent.Citation{
			Source: corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: citation.URI},
			Title:  citation.Title,
		}
		result = append(result, mapped)
	}
	return result
}

func protocolJSON(value any) (string, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func normalizeProtocolFinishReason(reason genai.FinishReason, hasToolCalls bool) corechat.FinishReason {
	if !isProtocolStopReason(reason) {
		return ""
	}
	switch reason {
	case genai.FinishReasonStop:
		if hasToolCalls {
			return corechat.FinishReasonToolCalls
		}
		return corechat.FinishReasonStop
	case genai.FinishReasonMaxTokens, genai.FinishReasonContinuation:
		return corechat.FinishReasonLength
	// Every reason where Gemini policy withheld the content belongs together,
	// recitation included: it stops generation for using an unauthorized
	// source, which is a policy outcome a caller acts on and not a fault with
	// no portable match.
	case genai.FinishReasonSafety, genai.FinishReasonBlocklist,
		genai.FinishReasonProhibitedContent, genai.FinishReasonSPII,
		genai.FinishReasonRecitation,
		genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent,
		genai.FinishReasonImageRecitation:
		return corechat.FinishReasonContentFilter
	// MALFORMED_FUNCTION_CALL, UNEXPECTED_TOOL_CALL, TOO_MANY_TOOL_CALLS, and
	// NO_IMAGE are generation faults, and LANGUAGE is a capability limit
	// ("stopped because of using an unsupported language", not a policy
	// withholding), so none has a portable match. OTHER and IMAGE_OTHER are
	// unspecified by Gemini itself, which is what Core's Other already means.
	// Every one of them keeps its provider value under native_finish_reason.
	default:
		return corechat.FinishReasonOther
	}
}

func isProtocolStopReason(reason genai.FinishReason) bool {
	return reason != "" && reason != genai.FinishReasonUnspecified
}

func mapProtocolUsage(usage *genai.GenerateContentResponseUsageMetadata) *corechat.Usage {
	if usage == nil {
		return nil
	}
	// Gemini reports tool-output prompt tokens and thought tokens outside the
	// similarly named prompt/candidate counters. Core totals include both, while
	// cache and reasoning remain optional breakdowns.
	mapped := corechat.Usage{
		InputTokens:  int64(usage.PromptTokenCount) + int64(usage.ToolUsePromptTokenCount),
		OutputTokens: int64(usage.CandidatesTokenCount) + int64(usage.ThoughtsTokenCount),
	}
	if usage.ThoughtsTokenCount != 0 {
		value := int64(usage.ThoughtsTokenCount)
		mapped.ReasoningTokens = &value
	}
	if usage.CachedContentTokenCount != 0 {
		value := int64(usage.CachedContentTokenCount)
		mapped.CacheReadInputTokens = &value
	}
	return &mapped
}

type protocolUsageExtension struct {
	PromptTokenCount        int32 `json:"prompt_token_count,omitzero"`
	CandidatesTokenCount    int32 `json:"candidates_token_count,omitzero"`
	ThoughtsTokenCount      int32 `json:"thoughts_token_count,omitzero"`
	ToolUsePromptTokenCount int32 `json:"tool_use_prompt_token_count,omitzero"`
	CachedContentTokenCount int32 `json:"cached_content_token_count,omitzero"`
	TotalTokenCount         int32 `json:"total_token_count,omitzero"`
}

func protocolUsageExtensionFrom(usage *genai.GenerateContentResponseUsageMetadata) protocolUsageExtension {
	return protocolUsageExtension{
		PromptTokenCount:        usage.PromptTokenCount,
		CandidatesTokenCount:    usage.CandidatesTokenCount,
		ThoughtsTokenCount:      usage.ThoughtsTokenCount,
		ToolUsePromptTokenCount: usage.ToolUsePromptTokenCount,
		CachedContentTokenCount: usage.CachedContentTokenCount,
		TotalTokenCount:         usage.TotalTokenCount,
	}
}
