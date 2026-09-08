package protocol

import (
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const (
	// ResponseExtensionKey preserves the complete official GenerateContent
	// response (or the current official stream chunk).
	ResponseExtensionKey = "google/response"
)

type protocolResponseMapper struct {
	partOffset   int
	provider     string
	lastPartKind corechat.PartKind
	finish       corechat.FinishReason
	hasToolCalls bool
}

func newProtocolResponseMapper(provider string) *protocolResponseMapper {
	return &protocolResponseMapper{provider: provider}
}

func (p *protocolResponseMapper) mapResponse(requestModel string, response *genai.GenerateContentResponse) (*corechat.Response, error) {
	if response == nil {
		return nil, errors.New("google: nil response")
	}
	if err := protocolPromptBlockError(response); err != nil {
		return nil, err
	}
	if len(response.Candidates) != 1 {
		return nil, fmt.Errorf("google: response has %d candidates; Core supports one output", len(response.Candidates))
	}
	metadata, err := p.mapMetadata(requestModel, response)
	if err != nil {
		return nil, err
	}
	candidate, err := p.candidate(response.Candidates[0])
	if err != nil {
		return nil, err
	}
	output, err := p.mapCandidate(candidate)
	if err != nil {
		return nil, fmt.Errorf("google: output: %w", err)
	}
	output.FinishReason = normalizeProtocolFinishReason(candidate.FinishReason, p.hasToolCalls)
	mapped := &corechat.Response{Output: output, Metadata: metadata}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("google: mapped response: %w", err)
	}
	return mapped, nil
}

func (p *protocolResponseMapper) mapDelta(requestModel string, response *genai.GenerateContentResponse) (*corechat.ResponseDelta, error) {
	if response == nil {
		return nil, errors.New("google: nil stream response")
	}
	if err := protocolPromptBlockError(response); err != nil {
		return nil, err
	}
	if len(response.Candidates) > 1 {
		return nil, fmt.Errorf("google: stream response has %d candidates; Core supports one output", len(response.Candidates))
	}
	metadata, err := p.mapMetadata(requestModel, response)
	if err != nil {
		return nil, err
	}
	mapped := &corechat.ResponseDelta{Metadata: metadata}
	if len(response.Candidates) == 1 {
		candidate, err := p.candidate(response.Candidates[0])
		if err != nil {
			return nil, err
		}
		if err := p.mapCandidateDelta(candidate, mapped); err != nil {
			return nil, fmt.Errorf("google: stream output: %w", err)
		}
		mapped.FinishReason = normalizeProtocolFinishReason(candidate.FinishReason, p.hasToolCalls)
		if mapped.FinishReason != "" {
			if p.finish != "" {
				return nil, errors.New("google: stream emitted more than one finish reason")
			}
			p.finish = mapped.FinishReason
			mapped.FinishReason = ""
		}
	}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("google: mapped response delta: %w", err)
	}
	return mapped, nil
}

func (p *protocolResponseMapper) finished() bool {
	return p.finish != ""
}

func (p *protocolResponseMapper) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || p.finish == "" {
		return nil, fmt.Errorf("google: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	delta.FinishReason = p.finish
	if err := delta.Validate(); err != nil {
		return nil, fmt.Errorf("google: terminal stream response: %w", err)
	}
	return delta, nil
}

// protocolPromptBlockError reports a prompt Gemini refused to answer.
//
// Gemini returns no candidates at all only when something was wrong with the
// prompt, and it explains that case in promptFeedback.blockReason rather than
// in the candidate list. Checking the count first would turn a documented
// refusal into a complaint about response shape and drop the reason the
// provider supplied, so both mapping paths ask this question before they
// count candidates.
func protocolPromptBlockError(response *genai.GenerateContentResponse) error {
	feedback := response.PromptFeedback
	if feedback == nil || feedback.BlockReason == "" ||
		feedback.BlockReason == genai.BlockedReasonUnspecified {
		return nil
	}
	if feedback.BlockReasonMessage != "" {
		return fmt.Errorf("google: prompt blocked (%s): %s",
			feedback.BlockReason, feedback.BlockReasonMessage)
	}
	return fmt.Errorf("google: prompt blocked (%s)", feedback.BlockReason)
}

func (p *protocolResponseMapper) candidate(candidate *genai.Candidate) (*genai.Candidate, error) {
	if candidate == nil {
		return nil, errors.New("google: nil candidate")
	}
	if candidate.Index != 0 {
		return nil, fmt.Errorf("google: candidate index is %d, want 0", candidate.Index)
	}
	return candidate, nil
}

func (p *protocolResponseMapper) mapMetadata(requestModel string, response *genai.GenerateContentResponse) (*corechat.ResponseMetadata, error) {
	modelName := response.ModelVersion
	if modelName == "" {
		modelName = requestModel
	}
	metadata := &corechat.ResponseMetadata{ID: response.ResponseID, Model: modelName}
	if err := metadata.Extra.Set(protocolKey(p.provider, "response"), response); err != nil {
		return nil, fmt.Errorf("google: preserve native response: %w", err)
	}
	if response.UsageMetadata != nil {
		metadata.Usage = mapProtocolUsage(response.UsageMetadata)
		if err := metadata.Extra.Set(protocolKey(p.provider, "usage"), protocolUsageExtensionFrom(response.UsageMetadata)); err != nil {
			return nil, err
		}
		if response.UsageMetadata.ToolUsePromptTokenCount != 0 {
			if err := metadata.Extra.Set(protocolKey(p.provider, "tool_use_prompt_tokens"), int64(response.UsageMetadata.ToolUsePromptTokenCount)); err != nil {
				return nil, err
			}
		}
	}
	if response.ModelVersion != "" {
		if err := metadata.Extra.Set(protocolKey(p.provider, "model_version"), response.ModelVersion); err != nil {
			return nil, err
		}
	}
	if response.PromptFeedback != nil {
		if err := metadata.Extra.Set(protocolKey(p.provider, "prompt_feedback"), response.PromptFeedback); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func (p *protocolResponseMapper) mapCandidate(candidate *genai.Candidate) (*corechat.Output, error) {
	output := &corechat.Output{
		Metadata: &corechat.OutputMetadata{},
	}
	if candidate.FinishReason != "" {
		if err := output.Metadata.Extra.Set(protocolKey(p.provider, "native_finish_reason"), candidate.FinishReason); err != nil {
			return nil, err
		}
	}
	if len(candidate.SafetyRatings) > 0 {
		if err := output.Metadata.Extra.Set(protocolKey(p.provider, "safety_ratings"), candidate.SafetyRatings); err != nil {
			return nil, err
		}
	}
	if candidate.FinishMessage != "" {
		if err := output.Metadata.Extra.Set(protocolKey(p.provider, "finish_message"), candidate.FinishMessage); err != nil {
			return nil, err
		}
	}
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return output, nil
	}

	offset := p.partOffset
	parts := make([]corechat.Part, 0, len(candidate.Content.Parts))
	for partIndex, part := range candidate.Content.Parts {
		if part == nil {
			return nil, fmt.Errorf("content.parts[%d]: nil part", partIndex)
		}
		mapped, include, err := mapProtocolCandidatePart(p.provider, offset+partIndex, part)
		if err != nil {
			return nil, fmt.Errorf("content.parts[%d]: %w", partIndex, err)
		}
		if include {
			parts = append(parts, mapped)
			p.hasToolCalls = p.hasToolCalls || mapped.Kind == corechat.PartToolCall
		}
	}
	p.partOffset = offset + len(candidate.Content.Parts)
	attachProtocolCitations(parts, candidate.CitationMetadata)
	if len(parts) == 0 {
		return output, nil
	}
	output.Message = &corechat.Message{Role: corechat.RoleAssistant, Parts: parts}
	return output, nil
}

func mapProtocolCandidatePart(provider string, partIndex int, part *genai.Part) (corechat.Part, bool, error) {
	var mapped corechat.Part
	switch {
	case part.FunctionCall != nil:
		if part.FunctionCall.Name == "" {
			return corechat.Part{}, false, errors.New("function call has no name")
		}
		if len(part.FunctionCall.PartialArgs) != 0 {
			return corechat.Part{}, false, errors.New("structured partial function arguments have no portable JSON-fragment representation")
		}
		arguments, err := protocolToolArguments(part.FunctionCall.Args)
		if err != nil {
			return corechat.Part{}, false, fmt.Errorf("function call arguments: %w", err)
		}
		id := part.FunctionCall.ID
		if id == "" {
			id = fmt.Sprintf("%s%d", protocolGeneratedToolPrefixFor(provider), partIndex)
		}
		mapped = corechat.NewToolCallPart(corechat.ToolCall{ID: id, Name: part.FunctionCall.Name, Arguments: arguments})
	case part.Thought:
		mapped = corechat.NewReasoningPart(part.Text, part.ThoughtSignature)
	case part.Text != "":
		mapped = corechat.NewTextPart(part.Text)
	case part.InlineData != nil:
		value, err := media.NewBytes(part.InlineData.MIMEType, part.InlineData.Data)
		if err != nil {
			return corechat.Part{}, false, err
		}
		value.Name = part.InlineData.DisplayName
		mapped = corechat.NewMediaPart(value)
	case part.FileData != nil:
		value, err := media.NewURI(part.FileData.MIMEType, part.FileData.FileURI)
		if err != nil {
			return corechat.Part{}, false, err
		}
		value.Name = part.FileData.DisplayName
		mapped = corechat.NewMediaPart(value)
	default:
		return corechat.Part{}, false, nil
	}
	if err := mapped.Metadata.Set(protocolKey(provider, "native_part"), part); err != nil {
		return corechat.Part{}, false, fmt.Errorf("preserve native part: %w", err)
	}
	return mapped, true, nil
}

func (p *protocolResponseMapper) mapCandidateDelta(candidate *genai.Candidate, response *corechat.ResponseDelta) error {
	response.OutputMetadata = &corechat.OutputMetadata{}
	if candidate.FinishReason != "" {
		if err := response.OutputMetadata.Extra.Set(protocolKey(p.provider, "native_finish_reason"), candidate.FinishReason); err != nil {
			return err
		}
	}
	if len(candidate.SafetyRatings) > 0 {
		if err := response.OutputMetadata.Extra.Set(protocolKey(p.provider, "safety_ratings"), candidate.SafetyRatings); err != nil {
			return err
		}
	}
	if candidate.FinishMessage != "" {
		if err := response.OutputMetadata.Extra.Set(protocolKey(p.provider, "finish_message"), candidate.FinishMessage); err != nil {
			return err
		}
	}
	if candidate.Content == nil {
		return nil
	}
	offset := p.partOffset
	for partIndex, part := range candidate.Content.Parts {
		if part == nil {
			return fmt.Errorf("content.parts[%d]: nil part", partIndex)
		}
		delta, kind, include, err := mapProtocolCandidatePartDelta(p.provider, offset+partIndex, part)
		if err != nil {
			return fmt.Errorf("content.parts[%d]: %w", partIndex, err)
		}
		if include {
			response.Parts = append(response.Parts, delta)
			p.lastPartKind = kind
			p.hasToolCalls = p.hasToolCalls || kind == corechat.PartToolCall
		}
	}
	p.partOffset = offset + len(candidate.Content.Parts)
	if p.lastPartKind == corechat.PartText {
		for _, citation := range protocolCitations(candidate.CitationMetadata) {
			response.Parts = append(response.Parts, corechat.NewCitationDelta(citation))
		}
	}
	return nil
}

func mapProtocolCandidatePartDelta(provider string, partIndex int, part *genai.Part) (corechat.PartDelta, corechat.PartKind, bool, error) {
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
			id = fmt.Sprintf("%s%d", protocolGeneratedToolPrefixFor(provider), partIndex)
		}
		mapped = corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: id, Name: part.FunctionCall.Name, Arguments: arguments})
		kind = corechat.PartToolCall
	case part.Thought:
		mapped = corechat.NewReasoningDelta(part.Text, part.ThoughtSignature)
		kind = corechat.PartReasoning
	case part.Text != "":
		mapped = corechat.NewTextDelta(part.Text)
		kind = corechat.PartText
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
	default:
		return corechat.PartDelta{}, "", false, nil
	}
	if err := mapped.Metadata.Set(protocolKey(provider, "native_part"), part); err != nil {
		return corechat.PartDelta{}, "", false, fmt.Errorf("preserve native part: %w", err)
	}
	return mapped, kind, true, nil
}

func protocolToolArguments(arguments map[string]any) (string, error) {
	if arguments == nil {
		return "", nil
	}
	return protocolJSON(arguments)
}

func attachProtocolCitations(parts []corechat.Part, citations *genai.CitationMetadata) {
	mapped := protocolCitations(citations)
	if len(mapped) == 0 {
		return
	}
	for index := len(parts) - 1; index >= 0; index-- {
		if parts[index].Kind == corechat.PartText {
			parts[index].Citations = append(parts[index].Citations, mapped...)
			return
		}
	}
}

func protocolCitations(metadata *genai.CitationMetadata) []corechat.Citation {
	if metadata == nil {
		return nil
	}
	result := make([]corechat.Citation, 0, len(metadata.Citations))
	for _, citation := range metadata.Citations {
		if citation == nil || citation.URI == "" {
			continue
		}
		mapped := corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceURI, Value: citation.URI},
			Title:  citation.Title,
		}
		if mapped.Validate() == nil {
			result = append(result, mapped)
		}
	}
	return result
}

func protocolJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func normalizeProtocolFinishReason(reason genai.FinishReason, hasToolCalls bool) corechat.FinishReason {
	switch reason {
	case "", genai.FinishReasonUnspecified:
		return ""
	case genai.FinishReasonStop:
		if hasToolCalls {
			return corechat.FinishReasonToolCalls
		}
		return corechat.FinishReasonStop
	case genai.FinishReasonMaxTokens:
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
	// MALFORMED_FUNCTION_CALL, UNEXPECTED_TOOL_CALL, TOO_MANY_TOOL_CALLS,
	// LANGUAGE, and NO_IMAGE are generation faults or capability limits with no
	// portable match.
	default:
		return corechat.FinishReasonOther
	}
}

func mapProtocolUsage(usage *genai.GenerateContentResponseUsageMetadata) corechat.Usage {
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
	return mapped
}

type protocolUsageExtension struct {
	PromptTokenCount        int32 `json:"prompt_token_count,omitempty"`
	CandidatesTokenCount    int32 `json:"candidates_token_count,omitempty"`
	ThoughtsTokenCount      int32 `json:"thoughts_token_count,omitempty"`
	ToolUsePromptTokenCount int32 `json:"tool_use_prompt_token_count,omitempty"`
	CachedContentTokenCount int32 `json:"cached_content_token_count,omitempty"`
	TotalTokenCount         int32 `json:"total_token_count,omitempty"`
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
