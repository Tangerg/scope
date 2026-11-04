package anthropic

import (
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestReasoningClassificationAndReplayShareIssuerAdmission(t *testing.T) {
	for _, provider := range []string{ProviderID, "minimax"} {
		for _, block := range []anthropicsdk.ContentBlockUnion{
			{Type: protocolBlockThinking, Thinking: "private thought", Signature: "signature"},
			{Type: protocolBlockRedactedThinking, Data: "redacted"},
		} {
			parts, err := mapProtocolContent([]anthropicsdk.ContentBlockUnion{block}, provider, nil)
			if err != nil {
				t.Fatal(err)
			}
			kind, found, err := ReasoningBlockKindOf(parts[0])
			if err != nil {
				t.Fatal(err)
			}
			_, included, err := mapProtocolReasoningPart(0, parts[0], ProviderID)
			if err != nil {
				t.Fatal(err)
			}
			want := provider == ProviderID
			if found != want || included != want {
				t.Fatalf("issuer %q: classification=%t, replay=%t", provider, found, included)
			}
			if want && string(kind) != block.Type {
				t.Fatalf("kind=%q, want %q", kind, block.Type)
			}
		}
	}
	part, err := NewThinkingPart("thought", []byte("signature"))
	if err != nil {
		t.Fatal(err)
	}
	part.Kind = corechat.PartText
	part.ReasoningState = nil
	if kind, found, err := ReasoningBlockKindOf(part); err != nil || found || kind != "" {
		t.Fatalf("text classified as reasoning: %q, %t, %v", kind, found, err)
	}
}
