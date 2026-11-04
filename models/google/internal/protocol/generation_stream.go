package protocol

import (
	"errors"
	"fmt"

	"google.golang.org/genai"
)

// A native invocation has one candidate and one terminal event. Advancing a
// copy lets modality decoders reject content before publishing envelope state.
type generationStream struct {
	responseID    string
	modelVersion  string
	finishReason  genai.FinishReason
	candidateSeen bool
}

func (g generationStream) advance(response *genai.GenerateContentResponse) (generationStream, error) {
	if response == nil {
		return g, errors.New("nil generation response")
	}
	if response.ResponseID != "" && g.responseID != "" && response.ResponseID != g.responseID {
		return g, fmt.Errorf("response id changed from %q to %q", g.responseID, response.ResponseID)
	}
	if response.ModelVersion != "" && g.modelVersion != "" && response.ModelVersion != g.modelVersion {
		return g, fmt.Errorf("model version changed from %q to %q", g.modelVersion, response.ModelVersion)
	}
	candidate, err := singleGenerationCandidate(response)
	if err != nil {
		if _, refused := errors.AsType[*promptBlockError](err); refused && g.candidateSeen {
			return g, errors.New("prompt block after a generation candidate")
		}
		return g, err
	}
	if candidate != nil && g.finished() {
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			return g, errors.New("content after finish reason")
		}
		if isProtocolStopReason(candidate.FinishReason) {
			return g, errors.New("more than one finish reason")
		}
	}
	if response.ResponseID != "" {
		g.responseID = response.ResponseID
	}
	if response.ModelVersion != "" {
		g.modelVersion = response.ModelVersion
	}
	if candidate != nil && isProtocolStopReason(candidate.FinishReason) {
		g.finishReason = candidate.FinishReason
	}
	g.candidateSeen = g.candidateSeen || candidate != nil
	return g, nil
}

func (g generationStream) finished() bool { return g.finishReason != "" }

func singleGenerationCandidate(response *genai.GenerateContentResponse) (*genai.Candidate, error) {
	if response == nil {
		return nil, errors.New("nil generation response")
	}
	if err := protocolPromptBlockError(response); err != nil {
		if len(response.Candidates) != 0 {
			return nil, errors.New("prompt block also returned generation candidates")
		}
		return nil, err
	}
	if len(response.Candidates) > 1 {
		return nil, fmt.Errorf("generation returned %d candidates, want at most 1", len(response.Candidates))
	}
	if len(response.Candidates) == 0 {
		return nil, nil
	}
	candidate := response.Candidates[0]
	if candidate == nil {
		return nil, errors.New("nil generation candidate")
	}
	if candidate.Index != 0 {
		return nil, fmt.Errorf("generation candidate index is %d, want 0", candidate.Index)
	}
	return candidate, nil
}

// Prompt refusal is a valid provider outcome, distinct from a malformed envelope.
type promptBlockError struct {
	reason  genai.BlockedReason
	message string
}

// Gemini and Vertex use different default enum spellings on the wire.
const geminiBlockReasonUnspecified genai.BlockedReason = "BLOCK_REASON_UNSPECIFIED"

func (p *promptBlockError) Error() string {
	if p.message != "" {
		return fmt.Sprintf("google: prompt blocked (%s): %s", p.reason, p.message)
	}
	return fmt.Sprintf("google: prompt blocked (%s)", p.reason)
}

func protocolPromptBlockError(response *genai.GenerateContentResponse) *promptBlockError {
	feedback := response.PromptFeedback
	if feedback == nil || feedback.BlockReason == "" ||
		feedback.BlockReason == genai.BlockedReasonUnspecified || feedback.BlockReason == geminiBlockReasonUnspecified {
		return nil
	}
	return &promptBlockError{reason: feedback.BlockReason, message: feedback.BlockReasonMessage}
}
