package bedrock

import (
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
)

// ReasoningBlockKind identifies a Bedrock Converse reasoning-content variant.
type ReasoningBlockKind string

// The vocabulary is closed because these kinds classify reasoning blocks the
// service returns. A redacted block carries no readable text but must still
// round trip intact, so it cannot be folded into the text kind.
const (
	ReasoningBlockText     ReasoningBlockKind = chatReasoningText
	ReasoningBlockRedacted ReasoningBlockKind = chatReasoningRedacted
)

// NewReasoningPart preserves the signature Bedrock requires when reasoning is
// replayed in a later request.
func NewReasoningPart(text string, signature []byte) (corechat.Part, error) {
	if text == "" || len(signature) == 0 {
		return corechat.Part{}, errors.New("bedrock: reasoning text and signature are required")
	}
	part := corechat.NewReasoningPart(text, signature)
	if err := setReasoningKind(&part, chatReasoningText); err != nil {
		return corechat.Part{}, err
	}
	return part, nil
}

// NewRedactedReasoningPart preserves an opaque Bedrock reasoning block without
// pretending its content is readable text.
func NewRedactedReasoningPart(content []byte) (corechat.Part, error) {
	if len(content) == 0 {
		return corechat.Part{}, errors.New("bedrock: redacted reasoning content is required")
	}
	part := corechat.NewReasoningPart("", content)
	if err := setReasoningKind(&part, chatReasoningRedacted); err != nil {
		return corechat.Part{}, err
	}
	return part, nil
}

// ReasoningBlockKindOf reports whether part contains Bedrock-issued reasoning.
// Text without a signature is display-only and is omitted from later requests.
func ReasoningBlockKindOf(part corechat.Part) (ReasoningBlockKind, bool, error) {
	value, found, err := part.Metadata.Decode[string](chatReasoningKindKey)
	if err != nil {
		return "", found, fmt.Errorf("bedrock: reasoning metadata: %w", err)
	}
	if !found {
		return "", false, nil
	}
	kind := ReasoningBlockKind(value)
	switch kind {
	case ReasoningBlockText, ReasoningBlockRedacted:
		return kind, true, nil
	default:
		return "", true, fmt.Errorf("bedrock: unknown reasoning block kind %q", value)
	}
}

func setReasoningKind(part *corechat.Part, kind string) error {
	if err := part.Metadata.Set(chatReasoningKindKey, kind); err != nil {
		return fmt.Errorf("bedrock: preserve reasoning kind: %w", err)
	}
	return nil
}

func setReasoningDeltaState(part *corechat.PartDelta, kind string, index int32) error {
	if err := part.Metadata.Set(chatReasoningKindKey, kind); err != nil {
		return fmt.Errorf("bedrock: preserve reasoning delta kind: %w", err)
	}
	// Signatures and redacted bytes belong to one content block. Core may
	// concatenate chunks within it, but must not join independent blocks.
	if err := part.Metadata.Set(chatContentBlockIndexKey, index); err != nil {
		return fmt.Errorf("bedrock: preserve reasoning content block index: %w", err)
	}
	return nil
}
