package protocol

import (
	"errors"
	"fmt"

	"google.golang.org/genai"
)

// Speech and transcription have no partial-output result. Their terminal
// GenerateContent response must establish a successful single candidate.
func validateProtocolCompletion(response *genai.GenerateContentResponse) error {
	if response == nil {
		return errors.New("nil generation response")
	}
	if err := protocolPromptBlockError(response); err != nil {
		return err
	}
	if len(response.Candidates) != 1 {
		return fmt.Errorf("generation returned %d candidates, want 1", len(response.Candidates))
	}
	candidate := response.Candidates[0]
	if candidate == nil {
		return errors.New("nil generation candidate")
	}
	if candidate.Index != 0 {
		return fmt.Errorf("generation candidate index is %d, want 0", candidate.Index)
	}
	switch candidate.FinishReason {
	case genai.FinishReasonStop:
		return nil
	case "", genai.FinishReasonUnspecified:
		return fmt.Errorf("generation has no terminal finish reason (%q)", candidate.FinishReason)
	default:
		return fmt.Errorf("generation ended with %s", candidate.FinishReason)
	}
}
