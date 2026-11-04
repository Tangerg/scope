package anthropic

import (
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
)

// ReasoningBlockKind identifies the official Anthropic reasoning block carried
// by a Core reasoning part.
type ReasoningBlockKind string

// The vocabulary is closed because these kinds classify reasoning blocks the
// service returns. A redacted block carries no readable text but must still
// round trip intact for a later turn to remain valid, so it cannot be folded
// into the thinking kind.
const (
	ReasoningBlockThinking ReasoningBlockKind = "thinking"
	ReasoningBlockRedacted ReasoningBlockKind = "redacted_thinking"
)

// NewThinkingPart preserves the signature Anthropic requires when thinking is
// replayed in a later request.
func NewThinkingPart(text string, signature []byte) (corechat.Part, error) {
	if len(signature) == 0 {
		return corechat.Part{}, errors.New("anthropic: thinking signature is required")
	}
	part := corechat.NewReasoningPart(text, signature)
	if err := setProtocolReasoningState(&part, ProviderID, ReasoningBlockThinking); err != nil {
		return corechat.Part{}, err
	}
	return part, nil
}

// NewRedactedThinkingPart preserves an opaque Anthropic thinking block without
// pretending its content is readable text.
func NewRedactedThinkingPart(data []byte) (corechat.Part, error) {
	if len(data) == 0 {
		return corechat.Part{}, errors.New("anthropic: redacted thinking data is required")
	}
	part := corechat.NewReasoningPart("", data)
	if err := setProtocolReasoningState(&part, ProviderID, ReasoningBlockRedacted); err != nil {
		return corechat.Part{}, err
	}
	return part, nil
}

// ReasoningBlockKindOf reports whether part contains Anthropic-issued
// reasoning replay state and, when it does, which official block variant it is.
func ReasoningBlockKindOf(part corechat.Part) (ReasoningBlockKind, bool, error) {
	return reasoningBlockKindFor(part, ProviderID)
}

func reasoningBlockKindFor(part corechat.Part, provider string) (ReasoningBlockKind, bool, error) {
	if part.Kind != corechat.PartReasoning {
		return "", false, nil
	}
	issuer, found, err := part.Metadata.Decode[string](protocolReasoningProviderKey)
	if err != nil {
		return "", found, fmt.Errorf("anthropic: reasoning issuer: %w", err)
	}
	if !found || issuer != provider {
		return "", false, nil
	}
	kind, found, err := part.Metadata.Decode[ReasoningBlockKind](protocolReasoningKindKey)
	if err != nil {
		return "", found, fmt.Errorf("anthropic: reasoning metadata: %w", err)
	}
	if !found {
		return "", false, nil
	}
	switch kind {
	case ReasoningBlockThinking, ReasoningBlockRedacted:
		if len(part.ReasoningState) == 0 {
			return "", true, errors.New("anthropic: reasoning replay state is empty")
		}
		return kind, true, nil
	default:
		return "", true, fmt.Errorf("anthropic: unknown reasoning block kind %q", kind)
	}
}
